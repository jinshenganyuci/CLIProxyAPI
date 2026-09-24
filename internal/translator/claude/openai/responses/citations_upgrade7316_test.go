package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func citationsUpgrade7316Frame(t *testing.T, value map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte("data: "), raw...)
}

func citationsUpgrade7316Text(t *testing.T, index int, text string, citation map[string]any) [][]byte {
	t.Helper()
	frames := [][]byte{
		citationsUpgrade7316Frame(t, map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": ""}}),
		citationsUpgrade7316Frame(t, map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": text}}),
	}
	if citation != nil {
		frames = append(frames, citationsUpgrade7316Frame(t, map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "citations_delta", "citation": citation}}))
	}
	return append(frames, citationsUpgrade7316Frame(t, map[string]any{"type": "content_block_stop", "index": index}))
}

func citationsUpgrade7316Annotations(label string) []map[string]any {
	return []map[string]any{
		{"type": "char_location", "document_index": 1, "document_title": label + " 文档", "start_char_index": 4, "end_char_index": 8, "cited_text": "证据🙂"},
		{"type": "web_search_result_location", "url": "https://example.com/" + label, "title": label + " 页面", "cited_text": "한글 café", "encrypted_index": "opaque-" + label},
	}
}

func citationsUpgrade7316Chunks(t *testing.T, id, boundary, stopReason string) ([][]byte, string) {
	t.Helper()
	first := citationsUpgrade7316Annotations("first-" + id)
	last := citationsUpgrade7316Annotations("last-" + id)
	chunks := [][]byte{citationsUpgrade7316Frame(t, map[string]any{"type": "message_start", "message": map[string]any{"id": id}})}
	chunks = append(chunks, citationsUpgrade7316Text(t, 0, "甲🙂", first[0])...)
	chunks = append(chunks, citationsUpgrade7316Text(t, 1, "", nil)...)
	chunks = append(chunks, citationsUpgrade7316Text(t, 2, ": 첫째", first[1])...)
	var boundaryOutputType string
	switch boundary {
	case "tool_use":
		chunks = append(chunks,
			[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"call_1","name":"read","input":{}}}`),
			[]byte(`data: {"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"example\"}"}}`),
			[]byte(`data: {"type":"content_block_stop","index":3}`))
		boundaryOutputType = "function_call"
	case "thinking":
		chunks = append(chunks,
			[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"thinking","thinking":""}}`),
			[]byte(`data: {"type":"content_block_delta","index":3,"delta":{"type":"thinking_delta","thinking":"middle reasoning"}}`),
			[]byte(`data: {"type":"content_block_stop","index":3}`))
		boundaryOutputType = "reasoning"
	case "redacted_thinking":
		chunks = append(chunks,
			[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"redacted_thinking","data":"opaque-thinking"}}`),
			[]byte(`data: {"type":"content_block_stop","index":3}`))
		boundaryOutputType = "reasoning"
	case "web_search":
		chunks = append(chunks,
			[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"server_tool_use","id":"search_1","name":"web_search","input":{}}}`),
			[]byte(`data: {"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"reference\"}"}}`),
			[]byte(`data: {"type":"content_block_stop","index":3}`),
			[]byte(`data: {"type":"content_block_start","index":4,"content_block":{"type":"web_search_tool_result","tool_use_id":"search_1","content":[]}}`),
			[]byte(`data: {"type":"content_block_stop","index":4}`))
		boundaryOutputType = "web_search_call"
	case "unknown_server_tool":
		chunks = append(chunks,
			[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"server_tool_use","id":"future_1","name":"future_tool","input":{}}}`),
			[]byte(`data: {"type":"content_block_stop","index":3}`))
	}
	chunks = append(chunks, citationsUpgrade7316Text(t, 5, "后续:", last[0])...)
	chunks = append(chunks, citationsUpgrade7316Text(t, 6, " café", last[1])...)
	chunks = append(chunks,
		citationsUpgrade7316Frame(t, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason}, "usage": map[string]any{"output_tokens": 12}}),
		[]byte(`data: {"type":"message_stop"}`))
	return chunks, boundaryOutputType
}

func citationsUpgrade7316Check(t *testing.T, id, boundaryType, stopReason string, chunks, outputs [][]byte) {
	t.Helper()
	var terminal gjson.Result
	counts := make(map[string]int)
	messageDone := make(map[string]gjson.Result)
	partDone := make(map[string]gjson.Result)
	deltas := make(map[string]string)
	lastSequence := int64(0)
	for i, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		counts[event]++
		sequence := data.Get("sequence_number").Int()
		if sequence <= lastSequence {
			t.Fatalf("event %d has non-increasing sequence %d after %d", i, sequence, lastSequence)
		}
		lastSequence = sequence
		switch event {
		case "response.output_text.delta":
			deltas[data.Get("item_id").String()] += data.Get("delta").String()
		case "response.content_part.done":
			partDone[data.Get("item_id").String()] = data.Get("part")
		case "response.output_item.done":
			if data.Get("item.type").String() == "message" {
				messageDone[data.Get("item.id").String()] = data.Get("item")
			}
		case "response.completed", "response.incomplete":
			terminal = data.Get("response")
			if i != len(outputs)-1 {
				t.Fatalf("terminal event at %d followed by %d events", i, len(outputs)-i-1)
			}
		}
	}
	wantStatus := "completed"
	if stopReason == "max_tokens" {
		wantStatus = "incomplete"
	}
	if terminal.Get("id").String() != id || terminal.Get("status").String() != wantStatus {
		t.Fatalf("wrong terminal response: %s", terminal.Raw)
	}
	if counts["response.completed"]+counts["response.incomplete"] != 1 {
		t.Fatalf("terminal event counts: %v", counts)
	}
	for _, event := range []string{"response.content_part.added", "response.output_text.done", "response.content_part.done"} {
		if counts[event] != 2 {
			t.Fatalf("%s count = %d, want one per merged text group", event, counts[event])
		}
	}
	items := terminal.Get("output").Array()
	wantItems := 2
	if boundaryType != "" {
		wantItems++
	}
	if len(items) != wantItems || items[0].Get("type").String() != "message" || items[len(items)-1].Get("type").String() != "message" {
		t.Fatalf("output ordering/items = %s", terminal.Get("output").Raw)
	}
	if boundaryType != "" && items[1].Get("type").String() != boundaryType {
		t.Fatalf("boundary item = %s, want %s", items[1].Raw, boundaryType)
	}
	for i, message := range []gjson.Result{items[0], items[len(items)-1]} {
		wantText := []string{"甲🙂: 첫째", "后续: café"}[i]
		label := []string{"first-", "last-"}[i] + id
		if message.Get("content.#").Int() != 1 || message.Get("content.0.text").String() != wantText {
			t.Fatalf("message %d text/content = %s", i, message.Raw)
		}
		wantRaw, _ := json.Marshal(citationsUpgrade7316Annotations(label))
		if !reflect.DeepEqual(message.Get("content.0.annotations").Value(), gjson.ParseBytes(wantRaw).Value()) {
			t.Fatalf("message %d citation data changed: %s, want %s", i, message.Get("content.0.annotations").Raw, wantRaw)
		}
		messageID := message.Get("id").String()
		if !reflect.DeepEqual(messageDone[messageID].Value(), message.Value()) || !reflect.DeepEqual(partDone[messageID].Value(), message.Get("content.0").Value()) || deltas[messageID] != wantText {
			t.Fatalf("message %d streaming deltas/done/completed disagree", i)
		}
		status := "completed"
		if i == 1 {
			status = wantStatus
		}
		if message.Get("status").String() != status {
			t.Fatalf("message %d status = %s, want %s", i, message.Get("status").String(), status)
		}
	}
	nonstream := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chunks, []byte("\n")), nil)
	if gjson.GetBytes(nonstream, "status").String() != wantStatus || !reflect.DeepEqual(gjson.GetBytes(nonstream, "output").Value(), terminal.Get("output").Value()) {
		t.Fatalf("stream/nonstream output mismatch:\nstream: %s\nnonstream: %s", terminal.Raw, nonstream)
	}
}

func TestCitationsUpgrade7316AdjacentGroupsRespectBoundaries(t *testing.T) {
	for _, boundary := range []string{"tool_use", "thinking", "redacted_thinking", "web_search", "unknown_server_tool"} {
		for _, stopReason := range []string{"end_turn", "max_tokens"} {
			t.Run(boundary+"/"+stopReason, func(t *testing.T) {
				t.Parallel()
				id := strings.ReplaceAll(t.Name(), "/", "_")
				chunks, itemType := citationsUpgrade7316Chunks(t, id, boundary, stopReason)
				citationsUpgrade7316Check(t, id, itemType, stopReason, chunks, translateClaudeResponsesStreamThroughRegistry(chunks))
			})
		}
	}
}

func TestCitationsUpgrade7316InterleavedStreamsKeepIndependentState(t *testing.T) {
	var states [2]any
	var chunks [2][][]byte
	var outputs [2][][]byte
	for i := range chunks {
		chunks[i], _ = citationsUpgrade7316Chunks(t, fmt.Sprintf("independent_%d", i), "web_search", "end_turn")
	}
	for n := 0; n < len(chunks[0]); n++ {
		for i := range states {
			outputs[i] = append(outputs[i], ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunks[i][n], &states[i])...)
		}
	}
	for i := range states {
		citationsUpgrade7316Check(t, fmt.Sprintf("independent_%d", i), "web_search_call", "end_turn", chunks[i], outputs[i])
	}
}
