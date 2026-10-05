package chat_completions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade815CodexCitationsUseContentOriginsAcrossLifecycle(t *testing.T) {
	for _, citationEvent := range []string{"response.output_text.annotation.added", "response.output_text.done", "response.content_part.done", "response.output_item.done"} {
		t.Run(citationEvent, func(t *testing.T) {
			const citation = `{"type":"url_citation","url":"https://example.invalid/reused","title":"Source","start_index":0,"end_index":2}`
			const ignored = `,{"type":"file_citation","file_id":"file-1"},{"type":"url_citation","url":"https://example.invalid/negative","start_index":-1,"end_index":1},{"type":"url_citation","url":"https://example.invalid/reversed","start_index":2,"end_index":1}`
			var state any
			var text strings.Builder
			var annotations []gjson.Result
			emit := func(raw string) {
				t.Helper()
				wire := []byte("data: " + raw)
				before := bytes.Clone(wire)
				for _, out := range ConvertCodexResponseToOpenAI(t.Context(), "gpt-5.5", nil, nil, wire, &state) {
					text.WriteString(gjson.GetBytes(out, "choices.0.delta.content").String())
					annotations = append(annotations, gjson.GetBytes(out, "choices.0.delta.annotations").Array()...)
				}
				if !bytes.Equal(before, wire) {
					t.Fatal("citation conversion modified the caller event")
				}
			}
			emit(`{"type":"response.created","response":{"id":"response-citation","model":"gpt-5.5"}}`)
			emit(`{"type":"response.reasoning_summary_text.delta","delta":"reasoning does not offset answer text"}`)
			parts := []string{"前🙂", "引用"}
			var content []json.RawMessage
			for index, part := range parts {
				content = append(content, json.RawMessage(fmt.Sprintf(`{"type":"output_text","text":%q,"annotations":[%s%s]}`, part, citation, ignored)))
				emit(fmt.Sprintf(`{"type":"response.content_part.added","item_id":"message-one","output_index":1,"content_index":%d,"part":{"type":"output_text","text":""}}`, index))
				for _, letter := range part {
					emit(fmt.Sprintf(`{"type":"response.output_text.delta","item_id":"message-one","output_index":1,"content_index":%d,"delta":%q}`, index, string(letter)))
				}
				switch citationEvent {
				case "response.output_text.annotation.added":
					emit(fmt.Sprintf(`{"type":%q,"item_id":"message-one","output_index":1,"content_index":%d,"annotation_index":0,"annotation":%s}`, citationEvent, index, citation))
				case "response.output_text.done":
					emit(fmt.Sprintf(`{"type":%q,"item_id":"message-one","output_index":1,"content_index":%d,"text":%q,"annotations":[%s%s]}`, citationEvent, index, part, citation, ignored))
				case "response.content_part.done":
					emit(fmt.Sprintf(`{"type":%q,"item_id":"message-one","output_index":1,"content_index":%d,"part":%s}`, citationEvent, index, content[index]))
				}
			}
			encodedContent, errMarshal := json.Marshal(content)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			item := fmt.Sprintf(`{"id":"message-one","type":"message","content":%s}`, encodedContent)
			// Repeated final events must neither duplicate citations nor collapse
			// the same source cited in two different content blocks.
			for range 2 {
				emit(fmt.Sprintf(`{"type":"response.output_item.done","output_index":1,"item":%s}`, item))
			}
			terminal := fmt.Sprintf(`{"type":"response.completed","response":{"id":"response-citation","model":"gpt-5.5","output":[%s],"usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":2}}}}`, item)
			emit(terminal)
			if text.String() != "前🙂引用" || len(annotations) != 2 {
				t.Fatalf("stream text=%q annotations=%v, want 4 runes and 2 distinct block citations", text.String(), annotations)
			}
			for index, annotation := range annotations {
				if annotation.Get("start_index").Int() != int64(index*2) || annotation.Get("end_index").Int() != int64(index*2+2) || annotation.Get("url").String() != "https://example.invalid/reused" {
					t.Fatalf("citation %d does not refer to its emitted content: %s", index, annotation.Raw)
				}
			}
			nonStream := ConvertCodexResponseToOpenAINonStream(t.Context(), "gpt-5.5", nil, nil, []byte(terminal), nil)
			nonStreamAnnotations := gjson.GetBytes(nonStream, "choices.0.message.annotations").Array()
			if len(nonStreamAnnotations) != len(annotations) {
				t.Fatalf("non-stream lost a repeated source in another block: %s", nonStream)
			}
			for index, annotation := range annotations {
				if annotation.Raw != nonStreamAnnotations[index].Raw {
					t.Fatalf("stream/non-stream citation differs: %s / %s", annotation.Raw, nonStreamAnnotations[index].Raw)
				}
			}
			if gjson.GetBytes(nonStream, "usage.prompt_tokens").Int() != 13 || gjson.GetBytes(nonStream, "usage.completion_tokens").Int() != 5 || gjson.GetBytes(nonStream, "usage.prompt_tokens_details.cached_tokens").Int() != 7 || gjson.GetBytes(nonStream, "usage.completion_tokens_details.reasoning_tokens").Int() != 2 {
				t.Fatalf("citation processing changed usage: %s", nonStream)
			}
		})
	}
}
