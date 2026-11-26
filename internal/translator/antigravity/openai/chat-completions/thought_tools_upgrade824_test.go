package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestUpgrade824AntigravityThinkingAfterToolOmitsPhantomToolDelta(t *testing.T) {
	chunks := []string{
		`{"response":{"candidates":[{"content":{"parts":[{"text":"consider","thought":true}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"path":"fixture"},"willContinue":true}}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"continue thinking","thought":true}]}}],"cpaUsageMetadata":{"promptTokenCount":17,"candidatesTokenCount":3,"thoughtsTokenCount":11,"totalTokenCount":31}}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"args":{"limit":1}}}]}}]}}`,
		`{"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":17,"candidatesTokenCount":3,"thoughtsTokenCount":11,"totalTokenCount":31}}}`,
	}
	var state any
	var arguments strings.Builder
	for i, chunk := range chunks {
		outputs := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte(chunk), &state)
		wantOutputs := 1
		if i == 4 {
			wantOutputs = 2
		}
		if len(outputs) != wantOutputs {
			t.Fatalf("chunk %d returned %d outputs, want %d", i, len(outputs), wantOutputs)
		}
		out := outputs[0]
		tools := ojson.GetBytes(out, "choices.0.delta.tool_calls")
		switch i {
		case 1:
			if tools.Get("#").Int() != 1 || tools.Get("0.function.name").String() != "read_file" {
				t.Fatalf("real tool delta missing: %s", out)
			}
			arguments.WriteString(tools.Get("0.function.arguments").String())
		case 3, 4:
			tool := tools.Get("0")
			index := tool.Get("index")
			if tools.Get("#").Int() != 1 || !index.Exists() || index.Int() != 0 || tool.Get("id").Exists() || tool.Get("function.name").Exists() {
				t.Fatalf("chunk %d must only continue the original tool call: %s", i, out)
			}
			arguments.WriteString(tool.Get("function.arguments").String())
		default:
			if tools.Exists() {
				t.Fatalf("chunk %d contains phantom/null tool_calls: %s", i, out)
			}
		}
		if i == 2 && ojson.GetBytes(out, "choices.0.delta.reasoning_content").String() != "continue thinking" {
			t.Fatalf("thinking chunk lost content: %s", out)
		}
		if finish := ojson.GetBytes(out, "choices.0.finish_reason").String(); finish != "" {
			t.Fatalf("chunk %d finalized with a delta: %s", i, out)
		}
		if i == 4 {
			terminal := outputs[1]
			if ojson.GetBytes(terminal, "choices.0.delta").Raw != "{}" || ojson.GetBytes(terminal, "choices.0.finish_reason").String() != "tool_calls" ||
				ojson.GetBytes(terminal, "usage.completion_tokens").Int() != 14 || ojson.GetBytes(terminal, "usage.completion_tokens_details.reasoning_tokens").Int() != 11 {
				t.Fatalf("terminal lost real tool state or thought usage: %s", terminal)
			}
		}
	}
	if got := arguments.String(); got != `{"path":"fixture","limit":1}` {
		t.Fatalf("tool argument fragments did not form one JSON object: %q", got)
	}
	if got := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte("[DONE]"), &state); len(got) != 0 {
		t.Fatalf("DONE duplicated terminal: %q", got)
	}
}
