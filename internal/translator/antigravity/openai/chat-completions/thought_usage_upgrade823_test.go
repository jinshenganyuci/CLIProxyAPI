package chat_completions

import (
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade823AntigravityThoughtUsageAgreesAcrossTerminalPaths(t *testing.T) {
	for _, tokens := range []struct {
		name                 string
		candidates, thoughts int64
	}{
		{"visible_and_thoughts", 3, 11},
		{"thoughts_only", 0, 11},
		{"visible_only", 3, 0},
	} {
		for _, mode := range []string{"nonstream", "terminal_usage", "pending_usage_done"} {
			t.Run(tokens.name+"/"+mode, func(t *testing.T) {
				usage := fmt.Sprintf(`{"promptTokenCount":17,"candidatesTokenCount":%d,"thoughtsTokenCount":%d,"cachedContentTokenCount":5,"totalTokenCount":%d}`, tokens.candidates, tokens.thoughts, 17+tokens.candidates+tokens.thoughts)
				var state any
				var out []byte
				if mode == "nonstream" {
					raw := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":` + usage + `}}`)
					out = ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", nil, nil, raw, &state)
				} else {
					key, finish := "usageMetadata", `,"finishReason":"STOP"`
					if mode == "pending_usage_done" {
						key, finish = "cpaUsageMetadata", ""
					}
					raw := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}` + finish + `}],"` + key + `":` + usage + `}}`)
					chunks := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, raw, &state)
					if len(chunks) != 1 {
						t.Fatalf("content chunk count = %d", len(chunks))
					}
					out = chunks[0]
					terminal := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte("[DONE]"), &state)
					if mode == "pending_usage_done" {
						if gjson.GetBytes(out, "usage").Exists() || gjson.GetBytes(out, "choices.0.finish_reason").String() != "" || len(terminal) != 1 {
							t.Fatalf("pending usage finalized early or lost terminal: initial=%s terminal=%q", out, terminal)
						}
						out = terminal[0]
					} else if len(terminal) != 0 {
						t.Fatalf("duplicate terminal after usage: %q", terminal)
					}
					if again := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte("[DONE]"), &state); len(again) != 0 {
						t.Fatalf("repeated DONE duplicated usage: %q", again)
					}
				}
				for path, want := range map[string]int64{
					"usage.prompt_tokens": 17, "usage.prompt_tokens_details.cached_tokens": 5,
					"usage.completion_tokens":                          tokens.candidates + tokens.thoughts,
					"usage.completion_tokens_details.reasoning_tokens": tokens.thoughts,
					"usage.total_tokens":                               17 + tokens.candidates + tokens.thoughts,
				} {
					if got := gjson.GetBytes(out, path).Int(); got != want {
						t.Errorf("%s = %d, want %d: %s", path, got, want, out)
					}
				}
				if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "stop" {
					t.Fatalf("finish_reason = %q: %s", got, out)
				}
			})
		}
	}
}
