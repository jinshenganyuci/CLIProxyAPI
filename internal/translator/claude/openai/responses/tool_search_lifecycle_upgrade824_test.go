package responses

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestUpgrade824ToolSearchReloadPreservesCitationsNamespacesAndUsage(t *testing.T) {
	setClientToolSearchEnabledForTest(t, false)
	ctx := WithClientToolSearch(context.Background(), true)
	request := codexToolSearchRequest(codexUserTurn, codexSearchCall1, codexSearchOutput1)
	translated, errConvert := ConvertOpenAIResponsesRequestToClaudeWithCompatContext(ctx, "claude-test", request, true)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	if !gjson.GetBytes(translated, `tools.#(name=="mcp__codex_apps__calendar_create_event").defer_loading`).Bool() {
		t.Fatal("discovered namespace tool was not declared deferred")
	}
	citation := map[string]any{"type": "web_search_result_location", "url": "https://example.com/synthetic", "title": "Shared source", "cited_text": "甲🙂", "encrypted_index": "synthetic-citation-index"}
	frames := [][]byte{[]byte(`data: {"type":"message_start","message":{"id":"msg_reload","usage":{"input_tokens":7,"cache_read_input_tokens":11,"cache_creation_input_tokens":3,"output_tokens":0}}}`)}
	frames = append(frames, citationsUpgrade7316Text(t, 0, "甲🙂", citation)...)
	for _, frame := range claudeToolSearchStream[1 : len(claudeToolSearchStream)-2] {
		raw := []byte(frame[len("data: "):])
		raw, _ = sjson.SetBytes(raw, "index", gjson.GetBytes(raw, "index").Int()+1)
		frames = append(frames, append([]byte("data: "), raw...))
	}
	frames = append(frames, citationsUpgrade7316Text(t, 4, "后续 café", citation)...)
	frames = append(frames, []byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`), []byte(`data: {"type":"message_stop"}`))
	var state any
	var terminal gjson.Result
	counts := map[string]int{}
	for i, frame := range frames {
		// Both the process default and later caller contexts may change; the
		// already-created response state must retain the original request pin.
		SetClientToolSearchEnabled(i%2 == 0)
		chunkCtx := ctx
		if i > 0 {
			chunkCtx = WithClientToolSearch(context.Background(), false)
		}
		for _, chunk := range ConvertClaudeResponseToOpenAIResponses(chunkCtx, "claude-test", request, translated, frame, &state) {
			event, data := parseClaudeResponsesSSEEvent(t, chunk)
			counts[event]++
			if event == "response.completed" {
				terminal = data.Get("response")
			}
		}
	}
	if counts["response.completed"] != 1 || counts["response.output_item.done"] != 5 {
		t.Fatalf("response lifecycle changed after reload: %v", counts)
	}
	if again := ConvertClaudeResponseToOpenAIResponses(ctx, "claude-test", request, translated, []byte(`data: {"type":"message_stop"}`), &state); len(again) != 0 {
		t.Fatalf("duplicate terminal emitted %q", again)
	}
	items := terminal.Get("output").Array()
	if len(items) != 5 {
		t.Fatalf("tool/citation boundaries changed: %s", terminal.Raw)
	}
	assertCodexToolSearchCall(t, "reloaded", items[1])
	assertOrdinaryToolCalls(t, "reloaded", items[2], items[3])
	for i, index := range []int{0, 4} {
		item := items[index]
		if item.Get("type").String() != "message" || item.Get("content.0.text").String() != []string{"甲🙂", "后续 café"}[i] || item.Get("content.0.annotations.#").Int() != 1 || !reflect.DeepEqual(item.Get("content.0.annotations.0").Value(), citation) {
			t.Fatalf("citation content was deduplicated across tool boundaries: %s", item.Raw)
		}
	}
	for path, want := range map[string]int64{"usage.input_tokens": 21, "usage.input_tokens_details.cached_tokens": 11, "usage.output_tokens": 5, "usage.total_tokens": 26} {
		if got := terminal.Get(path).Int(); got != want {
			t.Errorf("%s = %d, want %d", path, got, want)
		}
	}
	SetClientToolSearchEnabled(false)
	nonstream := ConvertClaudeResponseToOpenAIResponsesNonStream(ctx, "claude-test", request, translated, bytes.Join(frames, []byte("\n")), nil)
	if !reflect.DeepEqual(gjson.GetBytes(nonstream, "output").Value(), terminal.Get("output").Value()) {
		t.Fatalf("stream/non-stream disagreed under pinned tool search:\nstream=%s\nnonstream=%s", terminal.Raw, nonstream)
	}
	// A zero reasoning count is optional in the non-stream representation.
	for _, path := range []string{"usage.input_tokens", "usage.input_tokens_details.cached_tokens", "usage.output_tokens", "usage.total_tokens", "usage.output_tokens_details.reasoning_tokens"} {
		if gjson.GetBytes(nonstream, path).Int() != terminal.Get(path).Int() {
			t.Errorf("stream/non-stream usage differs at %s", path)
		}
	}
}
