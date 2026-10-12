package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade824AntigravityThinkingAfterToolOmitsPhantomToolDelta(t *testing.T) {
	chunks := []string{
		`{"response":{"candidates":[{"content":{"parts":[{"text":"consider","thought":true}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"path":"fixture"}}}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"continue thinking","thought":true}]}}],"cpaUsageMetadata":{"promptTokenCount":17,"candidatesTokenCount":3,"thoughtsTokenCount":11,"totalTokenCount":31}}}`,
		`{"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":17,"candidatesTokenCount":3,"thoughtsTokenCount":11,"totalTokenCount":31}}}`,
	}
	var state any
	for i, chunk := range chunks {
		outputs := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte(chunk), &state)
		if len(outputs) != 1 {
			t.Fatalf("chunk %d returned %d outputs", i, len(outputs))
		}
		out := outputs[0]
		tools := gjson.GetBytes(out, "choices.0.delta.tool_calls")
		if i == 1 {
			if tools.Get("#").Int() != 1 || tools.Get("0.function.name").String() != "read_file" {
				t.Fatalf("real tool delta missing: %s", out)
			}
		} else if tools.Exists() {
			t.Fatalf("chunk %d contains phantom/null tool_calls: %s", i, out)
		}
		if i == 2 && (gjson.GetBytes(out, "choices.0.delta.reasoning_content").String() != "continue thinking" || gjson.GetBytes(out, "choices.0.finish_reason").String() != "") {
			t.Fatalf("thinking chunk lost content or finalized early: %s", out)
		}
		if i == 3 && (gjson.GetBytes(out, "choices.0.finish_reason").String() != "tool_calls" || gjson.GetBytes(out, "usage.completion_tokens").Int() != 14 || gjson.GetBytes(out, "usage.completion_tokens_details.reasoning_tokens").Int() != 11) {
			t.Fatalf("terminal lost real tool state or thought usage: %s", out)
		}
	}
	if got := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte("[DONE]"), &state); len(got) != 0 {
		t.Fatalf("DONE duplicated terminal: %q", got)
	}
}
