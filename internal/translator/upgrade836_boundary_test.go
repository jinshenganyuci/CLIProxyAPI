package translator

import (
	"bytes"
	"fmt"
	"testing"

	clauderesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/claude"
	openaigemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/gemini"
	chatresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
)

func TestUpgrade836MissingIndexToolsCrossClaudeResponses(t *testing.T) {
	request := []byte(`{"stream":true,"tools":[{"name":"Weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`)
	chunks := []string{
		`data: {"id":"parallel836","model":"upstream","choices":[{"delta":{"tool_calls":[{"id":"call_paris","function":{"name":"weather","arguments":"{\"city\":"}}]}}]}`,
		`data: {"id":"parallel836","choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"Paris\"}"}}]}}]}`,
		`data: {"id":"parallel836","choices":[{"delta":{"tool_calls":[{"id":"call_tokyo","function":{"name":"weather","arguments":"{\"city\":"}}]}}]}`,
		`data: {"id":"parallel836","choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"Tokyo\"}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":6,"total_tokens":26,"prompt_tokens_details":{"cached_tokens":4}}}`,
		`data: [DONE]`,
	}
	var claudeState, responseState any
	var terminal gjson.Result
	var stop string
	responsesRequest := []byte(`{"tools":[{"type":"function","name":"Weather","parameters":{"type":"object"}}]}`)
	for _, chunk := range chunks {
		for _, claudeOutput := range openaiclaude.ConvertOpenAIResponseToClaude(t.Context(), "upstream", request, nil, []byte(chunk), &claudeState) {
			for _, line := range bytes.Split(claudeOutput, []byte("\n")) {
				if !bytes.HasPrefix(line, []byte("data: ")) {
					continue
				}
				if parsed := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: "))); parsed.Get("type").String() == "message_delta" {
					stop = parsed.Get("delta.stop_reason").String()
				}
				for _, output := range clauderesponses.ConvertClaudeResponseToOpenAIResponses(t.Context(), "claude-test", responsesRequest, nil, line, &responseState) {
					for _, responseLine := range bytes.Split(output, []byte("\n")) {
						parsed := gjson.ParseBytes(bytes.TrimPrefix(responseLine, []byte("data: ")))
						if parsed.Get("type").String() == "response.completed" {
							terminal = parsed.Get("response")
						}
					}
				}
			}
		}
	}
	if stop != "tool_use" || !terminal.Exists() {
		t.Fatalf("tool completion was lost across protocols: stop=%q terminal=%s", stop, terminal.Raw)
	}
	calls := terminal.Get("output").Array()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2: %s", len(calls), terminal.Raw)
	}
	for i, want := range []struct{ id, city string }{{"call_paris", "Paris"}, {"call_tokyo", "Tokyo"}} {
		if calls[i].Get("call_id").String() != want.id || calls[i].Get("name").String() != "Weather" || gjson.Get(calls[i].Get("arguments").String(), "city").String() != want.city {
			t.Fatalf("tool identity or arguments crossed: %s", calls[i].Raw)
		}
	}
	for field, want := range map[string]int64{"input_tokens": 20, "output_tokens": 6, "total_tokens": 26, "input_tokens_details.cached_tokens": 4} {
		if terminal.Get("usage."+field).Int() != want {
			t.Fatalf("usage %s = %d, want %d", field, terminal.Get("usage."+field).Int(), want)
		}
	}
}

func TestUpgrade836GeminiSignedParallelToolRoundTrip(t *testing.T) {
	var state any
	chunks := []string{
		`{"choices":[{"delta":{"content":"visible signature text","tool_calls":[{"index":1,"id":"unsigned","function":{"name":"inspect","arguments":"{\"thoughtSignature\":\"literal-data\"}"}},{"index":0,"id":"signed","function":{"name":"inspect","arguments":"{\"value\":1}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"opaque-sig836"}}}]},"finish_reason":"tool_calls"}]}`,
	}
	var parts []gjson.Result
	for _, chunk := range chunks {
		for _, output := range openaigemini.ConvertOpenAIResponseToGemini(t.Context(), "google/gemini-test", nil, nil, []byte(chunk), &state) {
			parts = append(parts, gjson.GetBytes(output, "candidates.0.content.parts").Array()...)
		}
	}
	if len(parts) != 3 || parts[0].Get("text").String() != "visible signature text" || parts[1].Get("functionCall.id").String() != "signed" || parts[2].Get("functionCall.id").String() != "unsigned" {
		t.Fatalf("mixed text/tool output or parallel order changed: %+v", parts)
	}
	if parts[1].Get("thoughtSignature").String() != "opaque-sig836" || parts[2].Get("thoughtSignature").Exists() || parts[2].Get("functionCall.args.thoughtSignature").String() != "literal-data" {
		t.Fatal("late signature moved to an unsigned sibling or changed literal arguments")
	}
	replay := []byte(fmt.Sprintf(`{"contents":[{"role":"model","parts":[%s,%s,%s]},{"role":"user","parts":[{"functionResponse":{"id":"signed","name":"inspect","response":{"result":"one"}}},{"functionResponse":{"id":"unsigned","name":"inspect","response":{"result":"two"}}}]}]}`, parts[0].Raw, parts[1].Raw, parts[2].Raw))
	out, errConvert := openaigemini.ConvertGeminiRequestToOpenAI("google/gemini-test", replay, false)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	calls := gjson.GetBytes(out, "messages.0.tool_calls").Array()
	if len(calls) != 2 || calls[0].Get("extra_content.google.thought_signature").String() != "opaque-sig836" || calls[1].Get("extra_content").Exists() || gjson.Get(calls[1].Get("function.arguments").String(), "thoughtSignature").String() != "literal-data" {
		t.Fatalf("Gemini history lost signed/unsigned distinction: %s", out)
	}
	if gjson.GetBytes(out, "messages.1.tool_call_id").String() != calls[0].Get("id").String() || gjson.GetBytes(out, "messages.2.tool_call_id").String() != calls[1].Get("id").String() {
		t.Fatalf("tool result pairings changed during replay: %s", out)
	}
}

func TestUpgrade836ReasoningUsageDialectsStreamParity(t *testing.T) {
	for _, tc := range []struct {
		name, details string
		want          int64
	}{
		{"chat", `"completion_tokens_details":{"reasoning_tokens":7}`, 7},
		{"responses", `"output_tokens_details":{"reasoning_tokens":5}`, 5},
		{"explicit_zero_precedence", `"output_tokens_details":{"reasoning_tokens":0},"completion_tokens_details":{"reasoning_tokens":7}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := fmt.Sprintf(`{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21,"prompt_tokens_details":{"cached_tokens":4},%s}`, tc.details)
			raw := []byte(fmt.Sprintf(`{"id":"usage836","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":%s}`, usage))
			plain := chatresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(t.Context(), "model", nil, nil, raw, nil)
			var state any
			var completed gjson.Result
			for _, chunk := range []string{`data: {"id":"usage836","model":"model","choices":[{"index":0,"delta":{"content":"answer"}}]}`, fmt.Sprintf(`data: {"id":"usage836","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":%s}`, usage), "data: [DONE]"} {
				for _, out := range chatresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(t.Context(), "model", nil, nil, []byte(chunk), &state) {
					for _, line := range bytes.Split(out, []byte("\n")) {
						p := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
						if p.Get("type").String() == "response.completed" {
							completed = p.Get("response")
						}
					}
				}
			}
			for _, result := range []gjson.Result{gjson.ParseBytes(plain), completed} {
				for field, want := range map[string]int64{"input_tokens": 12, "output_tokens": 9, "total_tokens": 21, "input_tokens_details.cached_tokens": 4, "output_tokens_details.reasoning_tokens": tc.want} {
					got := result.Get("usage." + field)
					// Streaming omits zero-valued reasoning details; an explicit
					// zero must still suppress the alternate dialect's nonzero value.
					if (!got.Exists() && want != 0) || got.Int() != want {
						t.Fatalf("usage %s = %s, want %d: %s", field, got.Raw, want, result.Raw)
					}
				}
			}
		})
	}
}
