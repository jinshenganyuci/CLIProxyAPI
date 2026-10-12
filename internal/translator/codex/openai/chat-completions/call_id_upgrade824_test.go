package chat_completions

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

func TestUpgrade824LongCallIDsPreserveUTF8AndCustomResultIdentity(t *testing.T) {
	for _, tc := range []struct{ name, id string }{{"ascii", strings.Repeat("a", 70)}, {"cjk", strings.Repeat("工具", 20)}, {"emoji", strings.Repeat("😀", 20)}} {
		longID := tc.id
		for _, customFirst := range []bool{false, true} {
			name := "function_first"
			if customFirst {
				name = "custom_first"
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				legalID := shortenCodexCallIDIfNeeded(strings.Repeat("a", 70))
				calls := []any{
					map[string]any{"id": longID, "type": "custom", "custom": map[string]any{"name": "run_script", "input": "print(1)"}},
					map[string]any{"id": legalID, "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{}`}},
				}
				if !customFirst {
					calls[0], calls[1] = calls[1], calls[0]
				}
				raw, errMarshal := json.Marshal(map[string]any{
					"messages": []any{
						map[string]any{"role": "user", "content": "run both tools"},
						map[string]any{"role": "assistant", "tool_calls": calls},
						map[string]any{"role": "tool", "tool_call_id": legalID, "content": "file result"},
						map[string]any{"role": "tool", "tool_call_id": longID, "content": "script result"},
					},
					"tools": []any{map[string]any{"type": "custom", "custom": map[string]any{"name": "run_script"}}},
				})
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				before := bytes.Clone(raw)
				out, errConvert := ConvertOpenAIRequestToCodex("codex-test", raw, true)
				if errConvert != nil {
					t.Fatal(errConvert)
				}
				if !utf8.Valid(out) || !json.Valid(out) || !bytes.Equal(raw, before) {
					t.Fatalf("invalid UTF-8/JSON or mutated original request: %q", out)
				}
				ids := map[string]string{}
				outputs := map[string]string{}
				for _, item := range gjson.GetBytes(out, "input").Array() {
					id := item.Get("call_id").String()
					if len(id) > 64 || !utf8.ValidString(id) {
						t.Fatalf("normalized call_id is invalid or exceeds 64 bytes: %q", id)
					}
					switch item.Get("type").String() {
					case "function_call", "custom_tool_call":
						ids[item.Get("name").String()] = id
					case "function_call_output", "custom_tool_call_output":
						outputs[item.Get("output").String()] = id
						if item.Get("output").String() == "script result" && item.Get("type").String() != "custom_tool_call_output" {
							t.Fatalf("custom result changed type: %s", item.Raw)
						}
					}
				}
				if len(ids) != 2 || len(outputs) != 2 || ids["run_script"] == ids["read_file"] || ids["read_file"] != legalID || outputs["script result"] != ids["run_script"] || outputs["file result"] != legalID {
					t.Fatalf("normalized call/results mismatched: calls=%v outputs=%v", ids, outputs)
				}
				again, errAgain := ConvertOpenAIRequestToCodex("codex-test", raw, true)
				if errAgain != nil || !bytes.Equal(out, again) {
					t.Fatalf("normalization is not deterministic: err=%v", errAgain)
				}
			})
		}
	}
}
