package signature

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload, 0
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
		message := `{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":""}]}`
		message, _ = sjson.Set(message, "content.0.text", text)
		kept = append(kept, message)
	}
	if restored == 0 {
		return payload, 0
	}
	out, errSet := sjson.SetRawBytes(payload, "input", []byte("["+strings.Join(kept, ",")+"]"))
	if errSet != nil {
		return payload, 0
	}
	return out, restored
}

func responsesCommentaryText(parts gjson.Result) string {
	if !parts.IsArray() {
		return ""
	}
	var text strings.Builder
	parts.ForEach(func(_, part gjson.Result) bool {
		if value := part.Get("text"); value.Type == gjson.String {
			text.WriteString(value.String())
		} else if part.Type == gjson.String {
			text.WriteString(part.String())
		}
		return true
	})
	return text.String()
}
