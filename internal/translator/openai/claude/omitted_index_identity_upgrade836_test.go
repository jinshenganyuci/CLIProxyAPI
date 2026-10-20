package claude

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade836RevisitedOmittedIndexIDPreservesParallelCalls(t *testing.T) {
	for _, explicitIndex := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit_index=%t", explicitIndex), func(t *testing.T) {
			index := func(n int) string {
				if explicitIndex {
					return fmt.Sprintf(`"index":%d,`, n)
				}
				return ""
			}
			events := runStream(t, streamReq,
				fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{%s"id":"a","function":{"name":"one","arguments":"{\"n\":"}}]}}]}`, index(0)),
				fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{%s"id":"b","function":{"name":"two","arguments":"{\"n\":"}}]}}]}`, index(1)),
				fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{%s"id":"a","function":{"arguments":"1"}}]}}]}`, index(0)),
				fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{%s"function":{"arguments":"}"}}]}}]}`, index(0)),
				fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{%s"id":"b","function":{"arguments":"2"}}]}}]}`, index(1)),
				fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{%s"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}`, index(1)),
			)
			assertSequentialContentBlocks(t, events)
			starts := toolUseStarts(events)
			if len(starts) != 2 {
				t.Fatalf("tool starts = %d, want 2", len(starts))
			}
			got := map[int]string{}
			for _, e := range events {
				if e.Type == "content_block_delta" && gjson.Get(e.Payload, "delta.type").String() == "input_json_delta" {
					idx := int(gjson.Get(e.Payload, "index").Int())
					got[idx] += gjson.Get(e.Payload, "delta.partial_json").String()
				}
			}
			for i, s := range starts {
				idx := int(gjson.Get(s.Payload, "index").Int())
				wantID := []string{"a", "b"}[i]
				if gjson.Get(s.Payload, "content_block.id").String() != wantID || !gjson.Valid(got[idx]) || gjson.Get(got[idx], "n").Int() != int64(i+1) {
					t.Fatalf("call %s arguments = %q, expected completed original call", wantID, got[idx])
				}
			}
			if countByType(events, "message_stop") != 1 || lastStopReason(events) != "tool_use" {
				t.Fatal("tool stream did not finish exactly once with intact arguments")
			}
		})
	}
}
