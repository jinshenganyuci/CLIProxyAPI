package claude

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade817GeminiSignedTextToolAndPartlessTerminalLifecycle(t *testing.T) {
	for _, finish := range []string{"STOP", "SAFETY", "MAX_TOKENS"} {
		t.Run(finish, func(t *testing.T) {
			request := []byte(`{"tools":[{"name":"read.file","input_schema":{"type":"object"}}]}`)
			frames := []string{
				`{"candidates":[{"content":{"parts":[{"thought":true,"text":"private","thoughtSignature":"thinking-signature"}]}}],"responseId":"response-original","modelVersion":"gemini-test"}`,
				`{"candidates":[{"content":{"parts":[{"text":"Visible🙂","thought_signature":"visible-carrier"}]}}]}`,
				`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"count":9007199254740993,"enabled":false}},"thoughtSignature":"tool-carrier"}]}}]}`,
				fmt.Sprintf(`{"candidates":[{"finishReason":%q}],"usageMetadata":{"promptTokenCount":13,"cachedContentTokenCount":4,"candidatesTokenCount":5,"thoughtsTokenCount":2}}`, finish),
				`[DONE]`,
			}
			var state any
			var events []gjson.Result
			for _, frame := range frames {
				raw := []byte(frame)
				before := bytes.Clone(raw)
				for _, out := range ConvertGeminiResponseToClaude(t.Context(), "gemini-test", request, request, raw, &state) {
					for _, line := range bytes.Split(out, []byte("\n")) {
						if bytes.HasPrefix(line, []byte("data: ")) {
							events = append(events, gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: "))))
						}
					}
				}
				if !bytes.Equal(before, raw) {
					t.Fatal("translator mutated upstream data")
				}
			}
			open := map[int64]string{}
			signatures := map[string]int{}
			var visible, thinking, args strings.Builder
			var starts, stops, finals int
			for _, event := range events {
				index := event.Get("index").Int()
				switch event.Get("type").String() {
				case "message_start":
					starts++
					if event.Get("message.id").String() != "response-original" {
						t.Fatal("response identity changed")
					}
				case "content_block_start":
					if len(open) != 0 {
						t.Fatalf("previous block was not closed: %s", event.Raw)
					}
					open[index] = event.Get("content_block.type").String()
				case "content_block_delta":
					if _, exists := open[index]; !exists {
						t.Fatalf("delta has no open block: %s", event.Raw)
					}
					switch event.Get("delta.type").String() {
					case "text_delta":
						visible.WriteString(event.Get("delta.text").String())
					case "thinking_delta":
						thinking.WriteString(event.Get("delta.thinking").String())
					case "signature_delta":
						if open[index] != "thinking" {
							t.Fatal("signature attached to visible/tool block")
						}
						signatures[event.Get("delta.signature").String()]++
					case "input_json_delta":
						args.WriteString(event.Get("delta.partial_json").String())
					}
				case "content_block_stop":
					if _, exists := open[index]; !exists {
						t.Fatalf("stop has no open block: %s", event.Raw)
					}
					delete(open, index)
				case "message_delta":
					finals++
					if event.Get("delta.stop_reason").String() != "tool_use" || event.Get("usage.input_tokens").Int() != 9 || event.Get("usage.output_tokens").Int() != 7 || event.Get("usage.cache_read_input_tokens").Int() != 4 {
						t.Fatalf("tool completion priority or usage accounting changed: %s", event.Raw)
					}
				case "message_stop":
					stops++
				}
			}
			if starts != 1 || finals != 1 || stops != 1 || len(open) != 0 || visible.String() != "Visible🙂" || thinking.String() != "private" {
				t.Fatalf("invalid complete lifecycle: starts=%d finals=%d stops=%d open=%v visible=%q thinking=%q", starts, finals, stops, open, visible.String(), thinking.String())
			}
			for _, sig := range []string{"thinking-signature", "visible-carrier", "tool-carrier"} {
				if signatures[sig] != 1 {
					t.Fatalf("signature %q count=%d", sig, signatures[sig])
				}
			}
			if gjson.Get(args.String(), "count").Raw != "9007199254740993" || gjson.Get(args.String(), "enabled").Raw != "false" {
				t.Fatal("tool arguments lost exact values")
			}
		})
	}
}
