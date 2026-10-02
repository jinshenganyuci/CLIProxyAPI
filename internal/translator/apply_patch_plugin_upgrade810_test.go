package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	claude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	gemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	interactions "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/interactions/responses"
	chat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// These hooks model an extension that would otherwise replace a failed output.
type upgrade810ResponseHooks struct {
	afterCalls int
	recover    bool
}

func (*upgrade810ResponseHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	return body
}

func (*upgrade810ResponseHooks) TranslateRequest(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*upgrade810ResponseHooks) NormalizeResponseBefore(_ context.Context, _, _ sdktranslator.Format, _ string, _, _, body []byte, _ bool) []byte {
	return body
}

func (*upgrade810ResponseHooks) TranslateResponse(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	return []byte(`{"status":"completed","plugin_recovered":true}`), true
}

func (h *upgrade810ResponseHooks) NormalizeResponseAfter(_ context.Context, _, _ sdktranslator.Format, _ string, _, _, body []byte, _ bool) []byte {
	h.afterCalls++
	if h.recover {
		return []byte(`{"status":"completed","plugin_recovered":true}`)
	}
	return body
}

const upgrade810PatchRequest = `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"read","parameters":{"type":"object"}}]}]}`
const upgrade810OrdinaryArguments = `{"n":9223372036854775807,"flag":false,"empty":null}`

func upgrade810Quote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestUpgrade810ApplyPatchNativeNonStreamPluginBoundary(t *testing.T) {
	for _, provider := range []struct {
		name      string
		format    sdktranslator.Format
		transform sdktranslator.ResponseNonStreamTransform
		body      func(string) []byte
	}{
		{"chat", sdktranslator.FormatOpenAI, chat.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream, func(arguments string) []byte {
			return []byte(fmt.Sprintf(`{"id":"r","choices":[{"message":{"tool_calls":[{"id":"call_9223372036854775807","type":"function","function":{"name":"functions__apply_patch","arguments":%s}},{"id":"ordinary","type":"function","function":{"name":"functions__read","arguments":%s}}]},"finish_reason":"tool_calls"}]}`, upgrade810Quote(arguments), upgrade810Quote(upgrade810OrdinaryArguments)))
		}},
		{"claude", sdktranslator.FormatClaude, claude.ConvertClaudeResponseToOpenAIResponsesNonStream, func(arguments string) []byte {
			return []byte(strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"r"}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_9223372036854775807","name":"functions__apply_patch","input":{}}}`,
				fmt.Sprintf(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, upgrade810Quote(arguments)),
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"ordinary","name":"functions__read","input":{}}}`,
				fmt.Sprintf(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":%s}}`, upgrade810Quote(upgrade810OrdinaryArguments)),
				`data: {"type":"content_block_stop","index":1}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
				`data: {"type":"message_stop"}`,
			}, "\n\n"))
		}},
		{"gemini", sdktranslator.FormatGemini, gemini.ConvertGeminiResponseToOpenAIResponsesNonStream, func(arguments string) []byte {
			return []byte(fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call_9223372036854775807","name":"functions__apply_patch","args":%s}},{"functionCall":{"id":"ordinary","name":"functions__read","args":%s}}]},"finishReason":"STOP"}]}`, arguments, upgrade810OrdinaryArguments))
		}},
		{"interactions", sdktranslator.FormatInteractions, interactions.ConvertInteractionsResponseToOpenAIResponsesNonStream, func(arguments string) []byte {
			return []byte(fmt.Sprintf(`{"id":"r","status":"completed","steps":[{"type":"function_call","id":"item_0","call_id":"call_9223372036854775807","name":"functions__apply_patch","arguments":%s},{"type":"function_call","id":"item_1","call_id":"ordinary","name":"functions__read","arguments":%s}]}`, arguments, upgrade810OrdinaryArguments))
		}},
	} {
		t.Run(provider.name, func(t *testing.T) {
			r := sdktranslator.NewRegistry()
			r.Register(sdktranslator.FormatOpenAIResponse, provider.format, nil, sdktranslator.ResponseTransform{NonStream: provider.transform})
			hooks := &upgrade810ResponseHooks{recover: true}
			r.SetPluginHooks(hooks)
			original := []byte(upgrade810PatchRequest)
			before := bytes.Clone(original)
			for _, invalid := range []string{`{"input":42}`, `{"input":"PRIVATE_PATCH","extra":true}`, `{"input":"\ud800"}`} {
				var state any
				raw := provider.body(invalid)
				rawBefore := bytes.Clone(raw)
				out := r.TranslateNonStream(context.Background(), provider.format, sdktranslator.FormatOpenAIResponse, "test", original, nil, raw, &state)
				errState, ok := state.(interface{ ToolInputError() error })
				if out != nil || !ok || errState.ToolInputError() == nil || hooks.afterCalls != 0 {
					t.Fatalf("invalid native tool input reached plugin recovery: output=%s state=%T after=%d", out, state, hooks.afterCalls)
				}
				if !bytes.Equal(raw, rawBefore) {
					t.Fatal("translation mutated the upstream response buffer")
				}
			}
			// Failed requests must not poison the next credential's independent state.
			hooks.recover = false
			var freshState any
			patch := "  *** Begin Patch\n*** Add File: 雪.txt\n+🙂 <>&\n*** End Patch\n "
			out := r.TranslateNonStream(context.Background(), provider.format, sdktranslator.FormatOpenAIResponse, "test", original, nil, provider.body(`{"input":`+upgrade810Quote(patch)+`}`), &freshState)
			items := gjson.GetBytes(out, "output").Array()
			if len(items) != 2 || items[0].Get("type").String() != "custom_tool_call" || items[0].Get("name").String() != "apply_patch" || items[0].Get("namespace").String() != "functions" || items[0].Get("input").String() != patch {
				t.Fatalf("patch identity, order or bytes changed: %s", out)
			}
			if items[0].Get("call_id").String() != "call_9223372036854775807" || items[1].Get("name").String() != "read" {
				t.Fatalf("call identities crossed between custom and ordinary tools: %s", out)
			}
			ordinaryID := items[1].Get("call_id").String()
			// Native Gemini synthesizes ordinary call IDs; patch IDs remain upstream IDs.
			if ordinaryID == "" || ordinaryID == items[0].Get("call_id").String() || (provider.format != sdktranslator.FormatGemini && ordinaryID != "ordinary") {
				t.Fatalf("ordinary call lost its native identity contract: %s", out)
			}
			arguments := gjson.Parse(items[1].Get("arguments").String())
			if arguments.Get("n").Raw != "9223372036854775807" || arguments.Get("flag").Raw != "false" || arguments.Get("empty").Raw != "null" || hooks.afterCalls != 1 || !bytes.Equal(original, before) {
				t.Fatalf("ordinary tool values, plugin invocation or request buffer changed: %s", out)
			}
		})
	}
}

func TestUpgrade810ApplyPatchStreamingFailureCannotBeRecoveredByPlugin(t *testing.T) {
	r := sdktranslator.NewRegistry()
	r.Register(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatInteractions, nil, sdktranslator.ResponseTransform{Stream: interactions.ConvertInteractionsResponseToOpenAIResponses})
	hooks := &upgrade810ResponseHooks{}
	r.SetPluginHooks(hooks)
	var state any
	feed := func(raw string) [][]byte {
		return r.TranslateStream(context.Background(), sdktranslator.FormatInteractions, sdktranslator.FormatOpenAIResponse, "devin/swe-2", []byte(upgrade810PatchRequest), nil, []byte(raw), &state)
	}
	feed(`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"item_0","call_id":"call_0","name":"functions__apply_patch"}}`)
	beforeFailure := hooks.afterCalls
	hooks.recover = true
	chunks := feed(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"input\":42}"}}`)
	if len(chunks) != 1 || !bytes.Contains(chunks[0], []byte(`"response.failed"`)) || bytes.Contains(chunks[0], []byte("plugin_recovered")) || hooks.afterCalls != beforeFailure {
		t.Fatalf("plugin normalized a terminal tool failure: chunks=%s after=%d", chunks, hooks.afterCalls)
	}
	for _, terminal := range []string{
		`{"event_type":"step.stop","index":0,"step":{"arguments":{"input":"later valid value"}}}`,
		`{"event_type":"interaction.completed","interaction":{"id":"r","steps":[]}}`,
		`[DONE]`,
	} {
		if chunks := feed(terminal); len(chunks) != 0 {
			t.Fatalf("failed request reopened: %s", chunks)
		}
	}
	errState, ok := state.(interface{ ToolInputError() error })
	if !ok || errState.ToolInputError() == nil || hooks.afterCalls != beforeFailure {
		t.Fatal("streaming error state was not retained")
	}
}
