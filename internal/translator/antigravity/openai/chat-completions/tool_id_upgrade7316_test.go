package chat_completions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/tidwall/gjson"
)

func TestToolIDUpgrade7316ParallelRequestsAndTurnsStayPaired(t *testing.T) {
	for request := range 8 {
		t.Run(fmt.Sprintf("request-%d", request), func(t *testing.T) {
			t.Parallel()
			ids := []string{"call/a", "call_a", "call_a_1"}
			messages := []any{map[string]any{"role": "user", "content": "run tools"}}
			for turn := range 2 {
				calls := make([]any, 0, len(ids))
				for i, id := range ids {
					calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": fmt.Sprintf("tool_%d", i), "arguments": `{"n":9223372036854775807,"ok":false}`}})
				}
				messages = append(messages, map[string]any{"role": "assistant", "tool_calls": calls})
				for i := len(ids) - 1; i >= 0; i-- {
					messages = append(messages, map[string]any{"role": "tool", "tool_call_id": ids[i], "content": fmt.Sprintf("request=%d turn=%d tool=%d <>&", request, turn, i)})
				}
			}
			raw, errMarshal := json.Marshal(map[string]any{"messages": messages})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			before := bytes.Clone(raw)
			out := ConvertOpenAIRequestToAntigravity("claude-sonnet-4-6", raw, false)
			contents := gjson.GetBytes(out, "request.contents").Array()
			if len(contents) != 5 {
				t.Fatalf("unexpected turn count: %s", out)
			}
			validID := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
			for turn := range 2 {
				seen := make(map[string]bool)
				calls, results := contents[1+turn*2].Get("parts").Array(), contents[2+turn*2].Get("parts").Array()
				if len(calls) != 3 || len(results) != 3 {
					t.Fatalf("tool pair count changed: %s", out)
				}
				for i := range calls {
					id := calls[i].Get("functionCall.id").String()
					if seen[id] || !validID.MatchString(id) || results[i].Get("functionResponse.id").String() != id {
						t.Fatalf("tool ID collision or mispairing: %s", out)
					}
					seen[id] = true
					if got := results[i].Get("functionResponse.response.result").String(); got != fmt.Sprintf("request=%d turn=%d tool=%d <>&", request, turn, i) {
						t.Fatalf("tool result belongs to another request/turn: %s", out)
					}
					if calls[i].Get("functionCall.args.n").Raw != "9223372036854775807" || calls[i].Get("functionCall.args.ok").Raw != "false" {
						t.Fatalf("tool arguments changed: %s", out)
					}
				}
			}
			if errPair := signature.ValidateGeminiFunctionCallPairing(out); errPair != nil {
				t.Fatal(errPair)
			}
			if !bytes.Equal(raw, before) {
				t.Fatal("caller input buffer changed")
			}
		})
	}
}
