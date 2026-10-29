package translator

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	geminichat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/chat-completions"
	geminiresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func upgrade847Events(chunks [][]byte) []gjson.Result {
	var events []gjson.Result
	for _, chunk := range chunks {
		for _, line := range bytes.Split(chunk, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, []byte("event:")) || len(line) == 0 {
				continue
			}
			line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if gjson.ValidBytes(line) {
				events = append(events, gjson.ParseBytes(line))
			}
		}
	}
	return events
}

func TestUpgrade847VertexPartialArgsAcrossProtocolsAndStreams(t *testing.T) {
	const schema = `{"type":"object","properties":{"reason":{"type":"string"},"_":{"type":"string"},"count":{"type":"integer"},"enabled":{"type":"boolean"},"nested":{"type":"object","properties":{"readOnly":{"type":"string"}}}}}`
	for _, format := range []string{"openai", "claude", "openai-response"} {
		t.Run(format, func(t *testing.T) {
			var request []byte
			switch format {
			case "openai":
				request = []byte(`{"messages":[{"role":"user","content":"inspect"}],"tools":[{"type":"function","function":{"name":"Inspect","parameters":` + schema + `}}]}`)
			case "claude":
				request = []byte(`{"messages":[{"role":"user","content":"inspect"}],"tools":[{"name":"Inspect","input_schema":` + schema + `}]}`)
			default:
				request = []byte(`{"input":"inspect","tools":[{"type":"function","name":"Inspect","parameters":` + schema + `}]}`)
			}
			before := bytes.Clone(request)
			translated := translator.Request(format, "gemini", "gemini-4-argon", request, true)
			var states [2]any
			var events [2][]gjson.Result
			// Interleave two requests with the same tool and upstream ID. Each
			// stream must keep its own argument tree and original schema.
			for step := 0; step < 3; step++ {
				for stream := 0; stream < 2; stream++ {
					var fc string
					switch step {
					case 0:
						fc = fmt.Sprintf(`{"id":"same-id","name":"Inspect","partialArgs":[{"jsonPath":"$.reason","stringValue":"stream-%d ","willContinue":true},{"jsonPath":"$.nested.readOnly","stringValue":"literal"}],"willContinue":true}`, stream)
					case 1:
						fc = `{"partial_args":[{"json_path":"$.reason","string_value":"cpa:commentary:v1"},{"json_path":"$['_']","string_value":"literal-placeholder-name"},{"json_path":"$.count","number_value":9007199254740993},{"json_path":"$.enabled","bool_value":false}],"will_continue":true}`
					default:
						fc = `{}`
					}
					raw := []byte(`{"responseId":"same-response","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":` + fc + `}]}}]}`)
					if step == 2 {
						raw, _ = sjson.SetBytes(raw, "candidates.0.finishReason", "STOP")
						raw, _ = sjson.SetRawBytes(raw, "usageMetadata", []byte(`{"promptTokenCount":13,"cachedContentTokenCount":3,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":20}`))
					}
					convert := geminichat.ConvertGeminiResponseToOpenAI
					if format == "claude" {
						convert = geminiclaude.ConvertGeminiResponseToClaude
					} else if format == "openai-response" {
						convert = geminiresponses.ConvertGeminiResponseToOpenAIResponses
						raw = append([]byte("data: "), raw...)
					}
					events[stream] = append(events[stream], upgrade847Events(convert(t.Context(), "gemini-4-argon", request, translated, raw, &states[stream]))...)
					if step == 2 {
						done := []byte("[DONE]")
						if format == "openai-response" {
							done = []byte("data: [DONE]")
						}
						events[stream] = append(events[stream], upgrade847Events(convert(t.Context(), "gemini-4-argon", request, translated, done, &states[stream]))...)
					}
				}
			}
			for stream := range states {
				var args, callID string
				starts, terminals := 0, 0
				var replay []byte
				for _, event := range events[stream] {
					switch format {
					case "openai":
						for _, call := range event.Get("choices.0.delta.tool_calls").Array() {
							if call.Get("id").Exists() {
								starts++
								callID = call.Get("id").String()
							}
							args += call.Get("function.arguments").String()
						}
						if event.Get("choices.0.finish_reason").String() == "tool_calls" {
							terminals++
						}
					case "claude":
						if event.Get("content_block.type").String() == "tool_use" {
							starts++
							callID = event.Get("content_block.id").String()
						}
						if event.Get("delta.type").String() == "input_json_delta" {
							args += event.Get("delta.partial_json").String()
						}
						if event.Get("type").String() == "message_stop" {
							terminals++
						}
					default:
						if event.Get("type").String() == "response.output_item.added" && event.Get("item.type").String() == "function_call" {
							starts++
							callID = event.Get("item.call_id").String()
						}
						if event.Get("type").String() == "response.function_call_arguments.delta" {
							args += event.Get("delta").String()
						}
						if event.Get("type").String() == "response.completed" {
							terminals++
							if event.Get("response.usage.input_tokens").Int() != 13 || event.Get("response.usage.output_tokens").Int() != 7 || event.Get("response.usage.total_tokens").Int() != 20 {
								t.Fatal("usage totals changed")
							}
						}
					}
				}
				if starts != 1 || terminals != 1 || callID == "" || !json.Valid([]byte(args)) {
					t.Fatalf("starts=%d terminals=%d id=%q args=%s", starts, terminals, callID, args)
				}
				if gjson.Get(args, "reason").String() != fmt.Sprintf("stream-%d cpa:commentary:v1", stream) || gjson.Get(args, "_").String() != "literal-placeholder-name" || gjson.Get(args, "count").Raw != "9007199254740993" || gjson.Get(args, "enabled").Type != gjson.False || gjson.Get(args, "nested.readOnly").String() != "literal" {
					t.Fatalf("arguments lost literal or stream isolation: %s", args)
				}
				switch format {
				case "claude":
					replay = []byte(fmt.Sprintf(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Inspect","input":%s}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}]}`, callID, args, callID))
				case "openai":
					replay = []byte(fmt.Sprintf(`{"messages":[{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":"Inspect","arguments":%q}}]},{"role":"tool","tool_call_id":%q,"content":"ok"}]}`, callID, args, callID))
				default:
					replay = []byte(fmt.Sprintf(`{"input":[{"type":"function_call","call_id":%q,"name":"Inspect","arguments":%q},{"type":"function_call_output","call_id":%q,"output":"ok"}]}`, callID, args, callID))
				}
				out := translator.Request(format, "gemini", "gemini-4-argon", replay, false)
				call := gjson.GetBytes(out, `contents.0.parts.0.functionCall`)
				if call.Get("name").String() != "Inspect" || call.Get("args.reason").String() != gjson.Get(args, "reason").String() || call.Get("args.count").Raw != "9007199254740993" {
					t.Fatalf("completed call did not replay: %s", out)
				}
			}
			if !bytes.Equal(before, request) {
				t.Fatal("caller request mutated")
			}
		})
	}
}

func TestUpgrade847VertexToolSearchSignatureReplay(t *testing.T) {
	payload := append([]byte{1, 0x8f, 0x3d, 0x6b, 0x5f}, bytes.Repeat([]byte{0xa7}, 400)...)
	sig := base64.StdEncoding.EncodeToString(payload)
	request := []byte(`{"input":"search","tools":[{"type":"tool_search","execution":"client","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}]}`)
	translated := translator.Request("openai-response", "gemini", "gemini-4-argon", request, true)
	chunks := []string{fmt.Sprintf(`data: {"responseId":"search847","candidates":[{"content":{"parts":[{"thoughtSignature":%q,"functionCall":{"id":"call847","name":"tool_search","partialArgs":[{"jsonPath":"$.query","stringValue":"literal cpa:commentary:v1","willContinue":true}],"willContinue":true}}]}}]}`, sig), `data: {"candidates":[{"content":{"parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.query","stringValue":" done"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`, `data: [DONE]`}
	var state any
	var completed gjson.Result
	var itemIDs []string
	for _, chunk := range chunks {
		for _, event := range upgrade847Events(geminiresponses.ConvertGeminiResponseToOpenAIResponses(t.Context(), "gemini-4-argon", request, translated, []byte(chunk), &state)) {
			if event.Get("item.type").String() == "tool_search_call" {
				itemIDs = append(itemIDs, event.Get("item.id").String())
			}
			if event.Get("type").String() == "response.completed" {
				completed = event.Get("response")
			}
		}
	}
	for _, id := range itemIDs {
		if id != "tsc_call847" {
			t.Fatalf("tool search ID=%s", id)
		}
	}
	if len(itemIDs) != 2 || !completed.Exists() {
		t.Fatal("missing tool search added/done or completed response")
	}
	input := completed.Get("output").Raw
	replay := []byte(`{"input":` + input + `}`)
	replay, _ = sjson.SetRawBytes(replay, "tools", []byte(gjson.GetBytes(request, "tools").Raw))
	out := translator.Request("openai-response", "gemini", "gemini-4-argon", replay, false)
	var fc gjson.Result
	for _, content := range gjson.GetBytes(out, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("functionCall").Exists() {
				fc = part
			}
		}
	}
	if fc.Get("functionCall.args.query").String() != "literal cpa:commentary:v1 done" || fc.Get("thoughtSignature").String() != sig {
		t.Fatalf("tool search replay lost args/signature: %s", out)
	}
	vertex := signature.SanitizeGeminiRequestThoughtSignatures(out, "contents")
	nonvertex := signature.SanitizeNonVertexGeminiRequestThoughtSignatures(out, "contents")
	if !bytes.Contains(vertex, []byte(sig)) || bytes.Contains(nonvertex, []byte(sig)) || !bytes.Contains(nonvertex, []byte("literal cpa:commentary:v1 done")) || !bytes.Contains(nonvertex, []byte(signature.GeminiSkipThoughtSignatureValidator)) {
		t.Fatal("Vertex endpoint scope or literal boundary lost")
	}
	payload[1] ^= 1
	if signature.IsGeminiVertexBareTinkSignature(base64.StdEncoding.EncodeToString(payload)) {
		t.Fatal("unknown key ID was classified as Vertex")
	}
	if strings.Count(string(nonvertex), signature.CodexCommentarySignatureMarker) != 1 {
		t.Fatal("literal commentary text changed")
	}
}

func TestUpgrade847VertexPartialArgsEOFFailureContract(t *testing.T) {
	for _, format := range []string{"openai", "claude", "openai-response"} {
		for _, mode := range []string{"incomplete", "invalid", "complete"} {
			t.Run(format+"/"+mode, func(t *testing.T) {
				request := []byte(`{"tools":[{"name":"Inspect","input_schema":{"type":"object","properties":{"text":{"type":"string"}}}}]}`)
				fc := `{"name":"Inspect","partialArgs":[{"jsonPath":"$.text","stringValue":"partial","willContinue":true}],"willContinue":true}`
				if mode == "invalid" {
					fc = `{"name":"Inspect","partialArgs":[{"jsonPath":"$.items[9999]","stringValue":"invalid"}],"willContinue":false}`
				} else if mode == "complete" {
					fc = `{"name":"Inspect","partialArgs":[{"jsonPath":"$.text","stringValue":"complete"}],"willContinue":false}`
				}
				raw := []byte(`{"responseId":"eof847","candidates":[{"index":0,"content":{"parts":[{"functionCall":` + fc + `}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":4,"totalTokenCount":16}}`)
				convert := geminichat.ConvertGeminiResponseToOpenAI
				if format == "claude" {
					convert = geminiclaude.ConvertGeminiResponseToClaude
				} else if format == "openai-response" {
					convert = geminiresponses.ConvertGeminiResponseToOpenAIResponses
					raw = append([]byte("data: "), raw...)
				}
				var state any
				convert(t.Context(), "gemini-4-argon", request, nil, raw, &state)
				contract, ok := state.(interface {
					FinalizeToolInput() [][]byte
					ToolInputError() error
				})
				if !ok {
					t.Fatal("translation state cannot report incomplete ordinary tool arguments at EOF")
				}
				if contract.ToolInputError() != nil {
					t.Fatal("ordinary partial arguments must retain the usage tail until EOF")
				}
				if frames := contract.FinalizeToolInput(); len(frames) != 0 {
					t.Fatal("EOF validation must not invent a protocol terminal frame")
				}
				if mode == "complete" {
					if contract.ToolInputError() != nil {
						t.Fatal("completed call was rejected")
					}
					return
				}
				err := contract.ToolInputError()
				if err == nil || err.Error() != "upstream stream ended with incomplete Vertex function call arguments" {
					t.Fatalf("EOF must expose a fixed sanitized error, got %v", err)
				}
				if frames := contract.FinalizeToolInput(); len(frames) != 0 || contract.ToolInputError() != err {
					t.Fatal("EOF validation must retain the original error without extra frames")
				}
			})
		}
	}
}
