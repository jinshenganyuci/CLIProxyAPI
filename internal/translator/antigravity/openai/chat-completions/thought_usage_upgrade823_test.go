package chat_completions

import (
	"context"
	"fmt"
	"testing"

	"github.com/MoeForge/moego/ojson"
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
					wantChunks := 2
					if mode == "pending_usage_done" {
						wantChunks = 1
					}
					if len(chunks) != wantChunks {
						t.Fatalf("chunk count = %d, want %d: %q", len(chunks), wantChunks, chunks)
					}
					content := chunks[0]
					if ojson.GetBytes(content, "choices.0.delta.content").String() != "hello" || ojson.GetBytes(content, "usage").Exists() || ojson.GetBytes(content, "choices.0.finish_reason").String() != "" {
						t.Fatalf("content delta lost text or finalized early: %s", content)
					}
					done := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte("[DONE]"), &state)
					if mode == "pending_usage_done" {
						if len(done) != 1 {
							t.Fatalf("expected one synthesized terminal: %q", done)
						}
						out = done[0]
					} else {
						if len(done) != 0 {
							t.Fatalf("duplicate terminal after usage: %q", done)
						}
						out = chunks[1]
					}
					if got := ojson.GetBytes(out, "choices.0.delta").Raw; got != "{}" {
						t.Fatalf("terminal contains a delta: %s", out)
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
					if got := ojson.GetBytes(out, path).Int(); got != want {
						t.Errorf("%s = %d, want %d: %s", path, got, want, out)
					}
				}
				if got := ojson.GetBytes(out, "choices.0.finish_reason").String(); got != "stop" {
					t.Fatalf("finish_reason = %q: %s", got, out)
				}
			})
		}
	}
}
