package signature

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade842ServerToolsSeparateFunctionSignatureGroups(t *testing.T) {
	native := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39})
	for _, key := range []string{"toolCall", "tool_call", "toolResponse", "tool_response"} {
		for _, mode := range []string{"synthetic", "first_native", "both_native"} {
			for _, thought := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/thought=%v", key, mode, thought), func(t *testing.T) {
					firstSignature, secondSignature := "", ""
					if mode != "synthetic" {
						firstSignature = fmt.Sprintf(`,"thoughtSignature":%q`, native)
					}
					if mode == "both_native" {
						secondSignature = fmt.Sprintf(`,"thoughtSignature":%q`, native)
					}
					server := fmt.Sprintf(`{%q:{"id":"server","toolType":"google_search","args":{"thoughtSignature":"literal-server"}},"thought":%v}`, key, thought)
					raw := []byte(fmt.Sprintf(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"first","args":{}}%s},%s,{"functionCall":{"name":"second","args":{"thoughtSignature":"literal-function"}}%s},{"functionCall":{"name":"sibling","args":{}}}]}]}`, firstSignature, server, secondSignature))
					out := SanitizeGeminiRequestThoughtSignatures(raw, "contents")
					if err := ValidateGeminiThoughtSignatures(out, GeminiThoughtSignatureValidationOptions{AllowBypassSentinel: true}); err != nil {
						t.Fatalf("sanitized replay failed validation: %v", err)
					}
					parts := gjson.GetBytes(out, "contents.0.parts").Array()
					if parts[1].Raw != server || parts[2].Get("functionCall.args.thoughtSignature").String() != "literal-function" {
						t.Fatal("signature grouping altered server payload or literal arguments")
					}
					wantFirst := GeminiSkipThoughtSignatureValidator
					if mode != "synthetic" {
						wantFirst = native
					}
					wantSecond := GeminiSkipThoughtSignatureValidator
					if thought {
						wantSecond = ""
					}
					if mode == "both_native" {
						wantSecond = native
					}
					if parts[0].Get("thoughtSignature").String() != wantFirst || parts[2].Get("thoughtSignature").String() != wantSecond || parts[3].Get("thoughtSignature").Exists() {
						t.Fatal("native signature priority, thought grouping, or unsigned sibling contract changed")
					}
					if again := SanitizeGeminiRequestThoughtSignatures(out, "contents"); &again[0] != &out[0] {
						t.Fatal("normalized group replay was not a no-op")
					}
				})
			}
		}
	}
}
