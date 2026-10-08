package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestToolResultCacheUpgrade7311ParallelResultsStayIsolated(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"one","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"two","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","cache_control":{"type":"ephemeral","ttl":"1h"},"content":[{"type":"text","text":"first","cache_control":{"type":"invalid"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="},"cache_control":{"type":"ephemeral","ttl":"5m"}}]},{"role":"tool","tool_call_id":"b","cache_control":{"type":"ephemeral","ttl":"1h"},"content":"second"}]}`)
	out, errConvert := ConvertOpenAIRequestToClaude("claude-opus-4-6", input, false)
	if errConvert != nil {
		t.Fatalf("request translation failed: %v", errConvert)
	}
	results := make(map[string]gjson.Result)
	for _, message := range gjson.GetBytes(out, "messages").Array() {
		for _, part := range message.Get("content").Array() {
			if part.Get("type").String() == "tool_result" {
				results[part.Get("tool_use_id").String()] = part
			}
		}
	}
	if len(results) != 2 {
		t.Fatalf("lost tool results: %s", out)
	}
	if results["a"].Get("cache_control.ttl").String() != "5m" || results["b"].Get("cache_control.ttl").String() != "1h" {
		t.Fatalf("cache scope mixed across tool results: %s", out)
	}
	if results["a"].Get("content.0.text").String() != "first" || results["a"].Get("content.1.source.data").String() != "aGVsbG8=" || results["b"].Get("content").String() != "second" {
		t.Fatalf("tool data changed: %s", out)
	}
	for _, result := range results {
		for _, part := range result.Get("content").Array() {
			if part.Get("cache_control").Exists() {
				t.Fatalf("nested unsupported cache control survived: %s", part.Raw)
			}
		}
	}
}
