package gemini

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade848UsageRestorationPreservesAuthorityAndLiteralFields(t *testing.T) {
	for _, body := range []string{
		`{"usageMetadata":{"totalTokenCount":1},"cpaUsageMetadata":{"totalTokenCount":19}}`,
		`{"cpaUsageMetadata":{"totalTokenCount":19},"usageMetadata":{"totalTokenCount":1}}`,
		`{"\u0063paUsageMetadata":{"totalTokenCount":19}}`,
		`{"cpaUsageMetadata":{"totalTokenCount":19},"\u0075sageMetadata":{"totalTokenCount":1}}`,
	} {
		t.Run(body, func(t *testing.T) {
			input := []byte(`{"response":` + body[:len(body)-1] + `,"candidates":[{"content":{"parts":[{"text":"cpa\u0055sageMetadata"},{"functionCall":{"name":"inspect","args":{"cpa\u0055sageMetadata":{"literal":true},"n":9007199254740993}}}]}}]}}`)
			before := bytes.Clone(input)
			for _, stream := range []bool{false, true} {
				var state any
				var out []byte
				if stream {
					ctx := context.WithValue(t.Context(), "alt", "")
					chunks := ConvertAntigravityResponseToGemini(ctx, "gemini-4-argon", nil, nil, input, &state)
					if len(chunks) != 1 {
						t.Fatalf("stream chunks = %d", len(chunks))
					}
					out = chunks[0]
				} else {
					out = ConvertAntigravityResponseToGeminiNonStream(t.Context(), "gemini-4-argon", nil, nil, input, &state)
				}
				count := 0
				gjson.ParseBytes(out).ForEach(func(key, value gjson.Result) bool {
					if key.String() == "usageMetadata" {
						count++
						if value.Get("totalTokenCount").Int() != 19 {
							t.Errorf("stream=%v usage authority lost: %s", stream, out)
						}
					}
					return true
				})
				if count != 1 || gjson.GetBytes(out, "cpaUsageMetadata").Exists() {
					t.Errorf("stream=%v expected one restored field: %s", stream, out)
				}
				if gjson.GetBytes(out, "candidates.0.content.parts.1.functionCall.args.cpaUsageMetadata.literal").Type != gjson.True || gjson.GetBytes(out, "candidates.0.content.parts.1.functionCall.args.n").Raw != "9007199254740993" {
					t.Errorf("literal arguments changed: %s", out)
				}
				if !bytes.Equal(input, before) {
					t.Fatal("caller response changed")
				}
			}
		})
	}
}
