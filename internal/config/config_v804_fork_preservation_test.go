package config

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestV804UnknownNestedArchiveKeepsForkIdentityAndPluginData(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_identity_%t", legacy), func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`codex:
  identity-confuse: %t
  credential-identity: {enabled: %t, synthesize-missing-installation-id: false}
  credential-proxy-policy: require
  future-fork-option: {enabled: false, amount: 0}
codex-api-key:
  - api-key: synthetic-upstream
    base-url: https://example.invalid/v1
    proxy-url: direct
    concurrency-limit: 3
plugins:
  configs:
    synthetic:
      arbitrary-options: {zero: 0, off: false, nested: {custom: retained}}
`, legacy, !legacy))
			untouched, changed, errRead := NormalizeConfigLayout(raw, false)
			if errRead != nil || changed || !bytes.Equal(untouched, raw) {
				t.Fatalf("legacy read changed file: changed=%t error=%v", changed, errRead)
			}
			migrated, changed, errMigrate := NormalizeConfigLayout(raw, true)
			if errMigrate != nil || !changed {
				t.Fatalf("migration failed: changed=%t error=%v", changed, errMigrate)
			}
			if errValidate := ValidateV8Config(migrated); errValidate != nil {
				t.Fatal(errValidate)
			}
			cfg, errParse := ParseConfigBytes(migrated)
			if errParse != nil {
				t.Fatal(errParse)
			}
			if cfg.Codex.IdentityConfuse != legacy || cfg.Codex.CredentialIdentity.Enabled == legacy || cfg.Codex.CredentialProxyPolicy != "require" {
				t.Fatalf("fork identity or proxy controls lost: %+v", cfg.Codex)
			}
			if len(cfg.CodexKey) != 1 || cfg.CodexKey[0].ProxyURL != "direct" || cfg.CodexKey[0].ConcurrencyLimit == nil || *cfg.CodexKey[0].ConcurrencyLimit != 3 {
				t.Fatalf("credential proxy or concurrency changed during migration: %+v", cfg.CodexKey)
			}
			var doc yaml.Node
			if errDecode := yaml.Unmarshal(migrated, &doc); errDecode != nil {
				t.Fatal(errDecode)
			}
			root := doc.Content[0]
			if yamlPath(root, "codex.future-fork-option") != nil || yamlPath(root, "oauth.providers.codex.future-fork-option") != nil || !strings.Contains(string(migrated), "#     future-fork-option: {enabled: false, amount: 0}") {
				t.Fatalf("unknown nested setting not archived: %s", migrated)
			}
			if yamlPath(root, "plugins.configs.synthetic.arbitrary-options.nested.custom") == nil {
				t.Fatal("plugin-owned nested setting was archived instead of preserved")
			}
			second, _, errAgain := NormalizeConfigLayout(migrated, true)
			if errAgain != nil || bytes.Count(second, []byte("future-fork-option")) != 1 {
				t.Fatalf("repeated migration duplicated the archive: %v", errAgain)
			}
		})
	}
}
