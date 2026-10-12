package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV823VertexInteractionsGroupInheritanceAndLegacyMigration(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      string
	}{
		{"omitted", "api-keys: {vertex: [{name: v, keys: [{api-key: synthetic}]}]}", "unset"},
		{"group-true", "api-keys: {vertex: [{name: v, interactions: true, keys: [{api-key: synthetic}]}]}", "true"},
		{"group-false", "api-keys: {vertex: [{name: v, interactions: false, keys: [{api-key: synthetic}]}]}", "false"},
		{"false-overrides-true", "api-keys: {vertex: [{name: v, interactions: true, keys: [{api-key: synthetic, interactions: false}]}]}", "false"},
		{"true-overrides-false", "api-keys: {vertex: [{name: v, interactions: false, keys: [{api-key: synthetic, interactions: true}]}]}", "true"},
		{"null-inherits", "api-keys: {vertex: [{name: v, interactions: true, keys: [{api-key: synthetic, interactions: null}]}]}", "true"},
		{"legacy-true", "vertex-api-key: [{api-key: synthetic, interactions: true}]", "true"},
		{"legacy-false", "vertex-api-key: [{api-key: synthetic, interactions: false}]", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			data, _, errNormalize := NormalizeConfigLayout([]byte(tc.raw), true)
			if errNormalize != nil {
				t.Fatal(errNormalize)
			}
			if errValidate := ValidateV8Config(data); errValidate != nil {
				t.Fatal(errValidate)
			}
			if errWrite := os.WriteFile(path, data, 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			loaded, errLoad := LoadConfig(path)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			if len(loaded.VertexCompatAPIKey) != 1 {
				t.Fatal("credential disappeared during migration")
			}
			actual := "unset"
			if value := loaded.VertexCompatAPIKey[0].Interactions; value != nil {
				actual = "false"
				if *value {
					actual = "true"
				}
			}
			if actual != tc.want {
				t.Fatalf("interactions=%s, want %s", actual, tc.want)
			}
		})
	}
}

func TestV823VertexInteractionsDoesNotRelaxOtherProviderSchemas(t *testing.T) {
	for _, provider := range []string{"codex", "gemini", "claude"} {
		t.Run(provider, func(t *testing.T) {
			raw := "config-version: 8\napi-keys: {" + provider + ": [{name: synthetic, interactions: true, keys: [{api-key: synthetic}]}]}\n"
			if errValidate := ValidateV8Config([]byte(raw)); errValidate == nil || !strings.Contains(errValidate.Error(), "unsupported group field interactions") {
				t.Fatalf("non-Vertex interactions group should remain invalid: %v", errValidate)
			}
		})
	}
}
