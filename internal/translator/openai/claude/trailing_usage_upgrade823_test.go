package claude

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade823ClaudeTerminalWaitsForAuthoritativeUsage(t *testing.T) {
	for _, content := range []struct {
		name, delta, finish, stop string
	}{
		{"text", `{"role":"assistant","content":"hello"}`, "stop", "end_turn"},
		{"thinking", `{"role":"assistant","reasoning_content":"consider this"}`, "stop", "end_turn"},
		{"tool", `{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]}`, "tool_calls", "tool_use"},
		{"truncated_tool", `{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}`, "length", "max_tokens"},
	} {
		for _, ending := range []string{"done", "trailing_empty_choices", "trailing_absent_choices"} {
			t.Run(content.name+"/"+ending, func(t *testing.T) {
				var state any
				var events []gjson.Result
				feed := func(payload string) []gjson.Result {
					t.Helper()
					var current []gjson.Result
					for _, chunk := range ConvertOpenAIResponseToClaude(context.Background(), "m", []byte(streamReq), nil, []byte("data: "+payload), &state) {
						for _, line := range strings.Split(string(chunk), "\n") {
							if strings.HasPrefix(line, "data: ") {
								current = append(current, gjson.Parse(strings.TrimPrefix(line, "data: ")))
							}
						}
					}
					events = append(events, current...)
					return current
				}
				feed(fmt.Sprintf(`{"id":"upgrade823","model":"m","choices":[{"index":0,"delta":%s}]}`, content.delta))
				finish := feed(fmt.Sprintf(`{"choices":[{"index":0,"delta":{},"finish_reason":%q}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`, content.finish))
				for _, event := range finish {
					if kind := event.Get("type").String(); kind == "message_delta" || kind == "message_stop" {
						t.Fatalf("preliminary finish usage emitted terminal event: %s", event.Raw)
					}
				}
				if ending != "done" {
					choices := ""
					if ending == "trailing_empty_choices" {
						choices = `"choices":[],`
					}
					feed(`{` + choices + `"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":80,"cache_write_tokens":10}}}`)
				}
				feed("[DONE]")
				if late := feed("[DONE]"); len(late) != 0 {
					t.Fatalf("repeated DONE emitted events: %+v", late)
				}
				if late := feed(`{"choices":[],"usage":{"prompt_tokens":999,"completion_tokens":999}}`); len(late) != 0 {
					t.Fatalf("usage after terminal emitted events: %+v", late)
				}
				counts := make(map[string]int)
				starts, stops := make(map[int64]int), make(map[int64]int)
				for _, event := range events {
					kind := event.Get("type").String()
					counts[kind]++
					switch kind {
					case "content_block_start":
						starts[event.Get("index").Int()]++
					case "content_block_stop":
						stops[event.Get("index").Int()]++
					case "message_delta":
						wantInput, wantCache, wantWrite := int64(100), int64(0), int64(0)
						if ending != "done" {
							wantInput, wantCache, wantWrite = 10, 80, 10
						}
						for path, want := range map[string]int64{"usage.input_tokens": wantInput, "usage.output_tokens": 20, "usage.cache_read_input_tokens": wantCache, "usage.cache_creation_input_tokens": wantWrite} {
							if got := event.Get(path).Int(); got != want {
								t.Errorf("%s = %d, want %d: %s", path, got, want, event.Raw)
							}
						}
						if got := event.Get("delta.stop_reason").String(); got != content.stop {
							t.Errorf("stop_reason = %q, want %q", got, content.stop)
						}
					}
				}
				if counts["message_start"] != 1 || counts["message_delta"] != 1 || counts["message_stop"] != 1 || events[len(events)-1].Get("type").String() != "message_stop" {
					t.Fatalf("terminal lifecycle counts = %+v", counts)
				}
				for index, count := range starts {
					if count != 1 || stops[index] != 1 {
						t.Errorf("block %d started %d times and stopped %d times", index, count, stops[index])
					}
				}
				if len(starts) != len(stops) {
					t.Fatalf("block lifecycle mismatch: starts=%v stops=%v", starts, stops)
				}
			})
		}
	}
}
