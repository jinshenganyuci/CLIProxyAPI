package signature

import (
	"bytes"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// RestoreResponsesCodexCommentaryMarkers consumes the internal carrier emitted
// when Codex commentary passes through Claude thinking and Responses reasoning.
// It restores assistant commentary before provider signature filtering can drop
// the text or mistake the marker for an opaque upstream signature. Only typed
// input reasoning items are inspected; visible text and tool payloads are literal.
func RestoreResponsesCodexCommentaryMarkers(payload []byte) ([]byte, int) {
	if !bytes.Contains(payload, []byte(CodexCommentarySignatureMarker)) && !bytes.Contains(payload, []byte(`\u`)) {
		return payload, 0
	}
	doc := ojson.NewDoc(payload)
	restored := RestoreResponsesCodexCommentaryMarkersDoc(doc)
	if restored == 0 {
		return payload, 0
	}
	return doc.Bytes(), restored
}

// RestoreResponsesCodexCommentaryMarkersDoc restores commentary in place.
func RestoreResponsesCodexCommentaryMarkersDoc(doc *ojson.Doc) int {
	if doc == nil {
		return 0
	}
	input := doc.Get("input")
	if !input.IsArray() || (!strings.Contains(input.Raw, CodexCommentarySignatureMarker) && !strings.Contains(input.Raw, `\u`)) {
		return 0
	}
	items := input.Array()
	kept := make([]string, 0, len(items))
	restored := 0
	for _, item := range items {
		if item.Get("type").String() != "reasoning" || !IsCodexCommentarySignatureMarker(item.Get("encrypted_content").String()) {
			kept = append(kept, item.Raw)
			continue
		}
		restored++
		text := responsesCommentaryText(item.Get("summary"))
		if text == "" {
			// SDKs can mirror summaries into content. Prefer summary to avoid
			// replaying the same commentary twice, as for normal reasoning.
			text = responsesCommentaryText(item.Get("content"))
		}
		if text == "" {
			continue
		}
		message := ojson.NewDocString(`{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":""}]}`)
		_ = message.Set("content.0.text", text)
		kept = append(kept, message.String())
	}
	if restored == 0 {
		return 0
	}
	if errSet := doc.SetRaw("input", "["+strings.Join(kept, ",")+"]"); errSet != nil {
		return 0
	}
	return restored
}

func responsesCommentaryText(parts ojson.Result) string {
	if !parts.IsArray() {
		return ""
	}
	var text strings.Builder
	parts.ForEach(func(_, part ojson.Result) bool {
		if value := part.Get("text"); value.Type == ojson.String {
			text.WriteString(value.String())
		} else if part.Type == ojson.String {
			text.WriteString(part.String())
		}
		return true
	})
	return text.String()
}
