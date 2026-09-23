package util_test

import (
	"bytes"
	"testing"

	codexclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	"github.com/tidwall/gjson"
)

func TestClaudeSchemaUpgrade7315NULOnlyAffectsSchemaPatterns(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"artifact","input_schema":{"type":"object","properties":{"unsafe":{"type":"string","pattern":"^[^\\0]*$"},"hex":{"type":"string","pattern":"^[^\\x00]*$"},"literal":{"type":"string","pattern":"^\\\\0$"},"sample":{"type":"object","default":{"pattern":"^[^\\0]*$"},"examples":[{"pattern":"^[^\\0]*$"}],"enum":[{"pattern":"^[^\\0]*$"}]}},"patternProperties":{"^[^\\0]*$":{"type":"string"},"^safe":{"type":"string"}}}}]}`)
	before := bytes.Clone(raw)
	for _, tt := range []struct {
		name, schemaPath string
		convert          func(string, []byte, bool) []byte
	}{
		{"chat", "tools.0.function.parameters", openaiclaude.ConvertClaudeRequestToOpenAI},
		{"codex", "tools.0.parameters", codexclaude.ConvertClaudeRequestToCodex},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := tt.convert("test-model", raw, false)
			schema := gjson.GetBytes(out, tt.schemaPath)
			if !schema.Exists() || schema.Get("properties.unsafe.pattern").Exists() || len(schema.Get("patternProperties").Map()) != 1 || !schema.Get("patternProperties").Map()["^safe"].Exists() {
				t.Fatalf("unsupported schema pattern cleanup failed: %s", out)
			}
			if schema.Get("properties.hex.pattern").String() != `^[^\x00]*$` || schema.Get("properties.literal.pattern").String() != `^\\0$` {
				t.Fatalf("supported pattern changed: %s", schema.Raw)
			}
			for _, path := range []string{"default.pattern", "examples.0.pattern", "enum.0.pattern"} {
				if schema.Get("properties.sample."+path).String() != `^[^\0]*$` {
					t.Fatalf("business data changed at %s: %s", path, schema.Raw)
				}
			}
			if !bytes.Equal(raw, before) {
				t.Fatal("caller request changed")
			}
		})
	}
}
