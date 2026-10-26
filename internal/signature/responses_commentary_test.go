package signature

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRestoreResponsesCodexCommentaryMarkersPreservesLiteralBoundaries(t *testing.T) {
	for _, marker := range []string{`cpa:commentary:v1`, ` cpa:commentary:v1 `, `\u0063pa:commentary:v1`} {
		t.Run(marker, func(t *testing.T) {
			input := []byte(`{"counter":9007199254740993,"input":[{"type":"reasoning","id":"discard-carrier-id","encrypted_content":"` + marker + `","summary":[{"type":"summary_text","text":"first "},"second"],"content":[{"type":"reasoning_text","text":"first second"}]},{"type":"reasoning","encrypted_content":"cpa:commentary:v1","content":[{"type":"reasoning_text","text":"fallback"}]},{"type":"reasoning","encrypted_content":"cpa:commentary:v1","summary":[]},{"type":"reasoning","encrypted_content":"provider-real-signature","summary":[{"text":"signed"}]},{"type":"function_call","arguments":"{\"encrypted_content\":\"cpa:commentary:v1\"}"},{"type":"message","role":"user","content":"cpa:commentary:v1"}]}`)
			before := bytes.Clone(input)
			out, count := RestoreResponsesCodexCommentaryMarkers(input)
			items := gjson.GetBytes(out, "input").Array()
			if count != 3 || len(items) != 5 || items[0].Get("phase").String() != "commentary" || items[0].Get("content.0.text").String() != "first second" || items[0].Get("id").Exists() || items[1].Get("content.0.text").String() != "fallback" {
				t.Fatalf("commentary restoration mismatch: %s", out)
			}
			originalItems := gjson.GetBytes(input, "input").Array()
			for i := 2; i < len(items); i++ {
				if items[i].Raw != originalItems[i+1].Raw {
					t.Fatal("provider signature or literal item was rewritten")
				}
			}
			if gjson.GetBytes(out, "counter").Raw != "9007199254740993" || !bytes.Equal(input, before) {
				t.Fatal("unrelated number or caller input was changed")
			}
			if again, n := RestoreResponsesCodexCommentaryMarkers(out); n != 0 || &again[0] != &out[0] {
				t.Fatal("marker consumption must be idempotent without copying")
			}
		})
	}
}
