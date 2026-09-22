package util

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestArrayCaseUpgrade7312UnionKeepsItemsAndExamples(t *testing.T) {
	for _, cleaner := range []struct {
		name  string
		clean func(string) string
	}{
		{"gemini", CleanJSONSchemaForGemini}, {"jsonschema", CleanJSONSchemaForGeminiJSONSchema}, {"antigravity", CleanJSONSchemaForAntigravity}, {"response", CleanJSONSchemaForAntigravityResponse},
	} {
		for _, typ := range []string{`"ARRAY"`, `["STRING","ARRAY"]`, `["ARRAY","STRING"]`, `["NULL","ARRAY"]`} {
			t.Run(cleaner.name+"/"+typ, func(t *testing.T) {
				input := fmt.Sprintf(`{"type":"OBJECT","properties":{"values":{"type":%s,"items":{"type":"STRING"}},"config":{"type":"OBJECT","default":{"items":[1,2],"type":"inventory"},"examples":[{"items":[3,4]}]},"limit":{"type":"INTEGER","maximum":9223372036854775807}}}`, typ)
				out := cleaner.clean(input)
				if !strings.EqualFold(gjson.Get(out, "properties.values.type").String(), "array") || !gjson.Get(out, "properties.values.items").Exists() {
					t.Fatalf("uppercase array union lost items: %s", out)
				}
				if cleaner.name == "jsonschema" {
					if gjson.Get(out, "properties.config.default.items.#").Int() != 2 || gjson.Get(out, "properties.config.examples.0.items.#").Int() != 2 {
						t.Fatalf("business examples changed: %s", out)
					}
					if gjson.Get(out, "properties.limit.maximum").Raw != "9223372036854775807" {
						t.Fatalf("integer constraint changed: %s", out)
					}
				}
			})
		}
	}
}
