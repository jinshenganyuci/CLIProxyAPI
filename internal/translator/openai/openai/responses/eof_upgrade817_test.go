package responses

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade817ResponsesEOFPreservesShellProvenanceAndLateUsage(t *testing.T) {
	for _, test := range []struct {
		name, action, finish string
		wantReady            bool
	}{
		{"finished shell", `{"commands":["pwd"],"timeout_ms":1000}`, "tool_calls", true},
		{"unfinished shell", `{"commands":["pwd"]}`, "", false},
		{"invalid action", `{"commands":[]}`, "tool_calls", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := []byte(shellRequest)
			translated := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test", request, true)
			toolName := gjson.GetBytes(translated, "tools.0.function.name").String()
			if toolName == "__cpa_local_shell" || toolName == "__cpa_local_shell_1" {
				t.Fatal("synthetic shell collided with user declaration")
			}
			var state any
			var events []gjson.Result
			push := func(frame string) {
				t.Helper()
				for _, output := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(t.Context(), "test", request, translated, []byte("data: "+frame), &state) {
					for _, line := range bytes.Split(output, []byte("\n")) {
						if bytes.HasPrefix(line, []byte("data: ")) {
							events = append(events, gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: "))))
						}
					}
				}
			}
			push(fmt.Sprintf(`{"id":"response-eof","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-original","type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, toolName, test.action))
			if test.finish != "" {
				push(fmt.Sprintf(`{"id":"response-eof","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, test.finish))
			}
			ready, ok := state.(interface{ CanFinalizeResponseStream() bool })
			if !ok || ready.CanFinalizeResponseStream() != test.wantReady {
				t.Fatalf("safe EOF completion readiness=%v, want %v", ok && ready.CanFinalizeResponseStream(), test.wantReady)
			}
			for _, event := range events {
				if event.Get("type").String() == "response.completed" {
					t.Fatal("response completed before transport finalization/late usage")
				}
			}
			if !test.wantReady {
				return
			}
			push(`{"id":"response-eof","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":5,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":4}}}`)
			if finalizer := state.(interface{ FinalizeToolInput() [][]byte }); len(finalizer.FinalizeToolInput()) != 0 {
				t.Fatal("valid finished shell rejected during EOF tool finalization")
			}
			push("[DONE]")
			push("[DONE]")
			count := 0
			for _, event := range events {
				if event.Get("type").String() != "response.completed" {
					continue
				}
				count++
				response := event.Get("response")
				if response.Get("output.0.type").String() != "shell_call" || response.Get("output.0.call_id").String() != "call-original" || response.Get("output.0.action.commands.0").String() != "pwd" || response.Get("usage.input_tokens").Int() != 13 || response.Get("usage.output_tokens").Int() != 5 || response.Get("usage.input_tokens_details.cached_tokens").Int() != 4 {
					t.Fatalf("EOF completion lost shell identity or late usage: %s", event.Raw)
				}
			}
			if count != 1 || ready.CanFinalizeResponseStream() {
				t.Fatalf("duplicate/incomplete EOF finalization: count=%d ready=%v", count, ready.CanFinalizeResponseStream())
			}
		})
	}
}
