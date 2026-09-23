package helps

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCodexOctalNULNormalizationPreservesPayloadAndLiteralPatterns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		removed bool
	}{
		{name: "octal_nul", pattern: `^[^\0]*$`, removed: true},
		{name: "literal_backslash_zero", pattern: `^[^\\0]*$`},
		{name: "hex_nul", pattern: `^[^\x00]*$`},
		{name: "unicode_nul", pattern: `^[^\u0000]*$`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, errMarshal := json.Marshal(map[string]any{
				"input":            []any{map[string]any{"role": "user", "content": tc.pattern}},
				"prompt_cache_key": "client-session",
				"tools": []any{map[string]any{
					"type": "namespace", "name": "files", "tools": []any{map[string]any{
						"type": "function", "name": "upload", "parameters": map[string]any{
							"type": "object", "properties": map[string]any{"file": map[string]any{
								"type": "string", "pattern": tc.pattern, "minLength": 1,
								"description":      tc.pattern,
								"default":          map[string]any{"pattern": tc.pattern},
								"enum":             []any{map[string]any{"pattern": tc.pattern}},
								"x-precise-number": json.Number("9007199254740993"),
							}},
						},
					}},
				}},
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			original := bytes.Clone(input)
			out := NormalizeCodexToolSchemas(input)
			if !json.Valid(out) || !bytes.Equal(input, original) {
				t.Fatal("schema normalization produced invalid JSON or mutated caller input")
			}
			prop := gjson.GetBytes(out, "tools.0.tools.0.parameters.properties.file")
			if prop.Get("pattern").Exists() == tc.removed {
				t.Fatalf("unexpected pattern presence: %s", prop.Raw)
			}
			if !tc.removed && prop.Get("pattern").String() != tc.pattern {
				t.Fatalf("supported/literal pattern changed: %s", prop.Raw)
			}
			for _, path := range []string{"description", "default.pattern", "enum.0.pattern"} {
				if got := prop.Get(path).String(); got != tc.pattern {
					t.Errorf("non-schema %s = %q, want %q", path, got, tc.pattern)
				}
			}
			if prop.Get("type").String() != "string" || prop.Get("minLength").Int() != 1 || prop.Get("x-precise-number").Raw != "9007199254740993" {
				t.Errorf("unrelated schema constraints changed: %s", prop.Raw)
			}
			if gjson.GetBytes(out, "input").Raw != gjson.GetBytes(input, "input").Raw || gjson.GetBytes(out, "prompt_cache_key").String() != "client-session" {
				t.Error("tool normalization changed the conversation or identity payload")
			}
			if !bytes.Equal(NormalizeCodexToolSchemas(out), out) {
				t.Error("normalization is not idempotent")
			}
		})
	}
}
