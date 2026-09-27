package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestV8ForkCredentialLimitStrictValidation(t *testing.T) {
	for _, provider := range []string{"gemini", "interactions", "vertex", "codex", "claude", "xai", "meta", "openai-compatibility"} {
		for _, limit := range []string{"3.5", "3.0", "\"3\"", "true", "0", "1000001"} {
			t.Run(provider+"/"+limit, func(t *testing.T) {
				raw := []byte(fmt.Sprintf("api-keys:\n  %s:\n    - name: synthetic\n      base-url: https://example.invalid\n      keys:\n        - api-key: synthetic\n          concurrency-limit: %s\n", provider, limit))
				if _, errParse := ParseConfigBytes(raw); errParse == nil {
					t.Fatalf("accepted noninteger or out-of-range concurrency-limit %s", limit)
				}
				path := filepath.Join(t.TempDir(), "config.yaml")
				if errWrite := os.WriteFile(path, raw, 0600); errWrite != nil {
					t.Fatal(errWrite)
				}
				if _, errLoad := LoadConfig(path); errLoad == nil {
					t.Fatal("LoadConfig accepted invalid concurrency-limit")
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(after, raw) {
					t.Fatal("invalid load changed configuration on disk")
				}
			})
		}
	}
}

func TestV8ForkAllCredentialFamiliesMigrate(t *testing.T) {
	for _, family := range v8KeyFamilies {
		for _, limit := range []string{"3", "10", "null", ""} {
			t.Run(family.current+"/"+limit, func(t *testing.T) {
				entry := "api-key: synthetic, proxy-url: direct"
				if limit != "" {
					entry += ", concurrency-limit: " + limit
				}
				raw := fmt.Sprintf("%s:\n  - {base-url: 'https://example.invalid', %s}\n", family.old, entry)
				if family.old == "openai-compatibility" {
					raw = fmt.Sprintf("openai-compatibility:\n  - name: synthetic\n    base-url: https://example.invalid\n    concurrency-limit: 8\n    api-key-entries: [{%s}]\n", entry)
				}
				before, errParse := ParseConfigBytes([]byte(raw))
				if errParse != nil {
					t.Fatal(errParse)
				}
				migrated, _, errMigrate := NormalizeConfigLayout([]byte(raw), true)
				if errMigrate != nil {
					t.Fatal(errMigrate)
				}
				if errValidate := ValidateV8Config(migrated); errValidate != nil {
					t.Fatal(errValidate)
				}
				after, errParse := ParseConfigBytes(migrated)
				if errParse != nil {
					t.Fatal(errParse)
				}
				assertForkConfigEquivalent(t, before, after)
			})
		}
	}
}

func TestV8ForkCredentialLimitMixedPrecedence(t *testing.T) {
	const raw = `codex-api-key: [{api-key: ignored, concurrency-limit: 3.5}]
api-keys:
  codex:
    - name: selected
      base-url: https://example.invalid
      keys: [{api-key: selected, concurrency-limit: 3}]
codex: {credential-proxy-policy: require, credential-identity: {enabled: true}}
oauth:
  providers:
    codex:
      credential-proxy-policy: prefer
      credential-identity: {enabled: false}
`
	cfg, errParse := ParseConfigBytes([]byte(raw))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if len(cfg.CodexKey) != 1 || cfg.CodexKey[0].APIKey != "selected" || *cfg.CodexKey[0].ConcurrencyLimit != 3 || cfg.Codex.CredentialIdentity.Enabled || cfg.Codex.CredentialProxyPolicy != "prefer" {
		t.Fatal("legacy settings overrode explicitly present v8 settings")
	}
}

func TestV8ForkMigrationRoundTripAndBackupRestore(t *testing.T) {
	const source = `# Fork settings must survive migration.
port: 8317
api-keys: [synthetic-client-a, synthetic-client-b, synthetic-client-c]
auth-dir: /synthetic/auths
proxy-url: socks5h://127.0.0.1:1080
trusted-proxies: [127.0.0.1]
request-retry: 2
codex:
  credential-proxy-policy: require
  credential-identity: {enabled: true, synthesize-missing-installation-id: false}
  disable-codex-cloaking: true
  response-steering: true
disable-image-generation: chat
gpt-image-2-base-model: gpt-6-luna
plugins:
  enabled: true
  configs:
    cpa-access-guard:
      # Preserve arbitrary plugin policy and zero values.
      enabled: true
      options: {image-billing: {price: 0, enabled: false}, proxy: direct, limits: [3, 2, 10], extra: null}
retired-unknown: {keep-for-reference: true}
codex-api-key:
  - api-key: synthetic-codex
    base-url: https://example.invalid/v1
    proxy-url: socks5h://127.0.0.1:1081
    concurrency-limit: 3
    disable-codex-cloaking: false
    models: [{name: gpt-6-luna, alias: alias, support-configuration-update: true}]
openai-compatibility:
  - name: synthetic-images
    base-url: https://example.invalid/v1
    concurrency-limit: 4
    models: [{name: synthetic-image, image: true, input-modalities: [text, image], output-modalities: [image], use-max-completion-tokens: true}]
    api-key-entries:
      - {api-key: synthetic-openai, proxy-url: direct, concurrency-limit: 2}
      - {api-key: synthetic-default, proxy-url: direct}
`
	raw := []byte(source)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, raw, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	before, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	saved, _ := os.ReadFile(path)
	if !bytes.Equal(saved, raw) {
		t.Fatal("legacy startup unexpectedly migrated configuration")
	}
	if errSave := SaveConfigPreserveComments(path, before, true); errSave != nil {
		t.Fatal(errSave)
	}
	migrated, _ := os.ReadFile(path)
	if errValidate := ValidateV8Config(migrated); errValidate != nil {
		t.Fatal(errValidate)
	}
	after, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	assertForkConfigEquivalent(t, before, after)
	if !after.OAuthOnlyFields["codex.credential-identity.enabled"] || !after.OAuthOnlyFields["codex.credential-proxy-policy"] {
		t.Fatal("migrated provider fields did not acquire native OAuth-only scope")
	}
	var doc yaml.Node
	if errDecode := yaml.Unmarshal(migrated, &doc); errDecode != nil {
		t.Fatal(errDecode)
	}
	for _, path := range []string{"requests.proxy-url", "oauth.providers.codex.credential-identity.enabled", "oauth.providers.codex.credential-proxy-policy", "api-keys.codex", "api-keys.openai-compatibility", "multimedia.disable-image-generation"} {
		if yamlPath(doc.Content[0], path) == nil {
			t.Fatalf("migration omitted %s", path)
		}
	}
	for _, old := range []string{"port", "proxy-url", "codex", "codex-api-key", "openai-compatibility", "retired-unknown"} {
		if yamlPath(doc.Content[0], old) != nil {
			t.Fatalf("legacy field remains active: %s", old)
		}
	}
	for _, comment := range []string{"Fork settings must survive migration.", "Preserve arbitrary plugin policy", "retired-unknown", "keep-for-reference"} {
		if !strings.Contains(string(migrated), comment) {
			t.Fatalf("migration lost comment %q", comment)
		}
	}
	if after.OpenAICompatibility[0].APIKeyEntries[1].ConcurrencyLimit != nil {
		t.Fatal("provider fallback concurrency was inherited by an omitted per-key limit")
	}
	// A legacy API save on a v8 file must persist values without restoring old paths.
	after.RequestRetry = 5
	if errSave := SaveConfigPreserveComments(path, after); errSave != nil {
		t.Fatal(errSave)
	}
	reloaded, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	assertForkConfigEquivalent(t, after, reloaded)
	// Rollback restores the original backup rather than asking v7 to parse v8 YAML.
	if errWrite := os.WriteFile(path, raw, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	restored, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	assertForkConfigEquivalent(t, before, restored)
	if len(restored.OAuthOnlyFields) != 0 {
		t.Fatal("restoring legacy backup retained v8-only scope")
	}
}

func assertForkConfigEquivalent(t *testing.T, want, got *Config) {
	t.Helper()
	var a, b yaml.Node
	if err := a.Encode((*legacyConfig)(want)); err != nil {
		t.Fatal(err)
	}
	if err := b.Encode((*legacyConfig)(got)); err != nil {
		t.Fatal(err)
	}
	var av, bv any
	if err := a.Decode(&av); err != nil {
		t.Fatal(err)
	}
	if err := b.Decode(&bv); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(av, bv) {
		t.Fatal("effective serialized settings changed across migration/save/restore")
	}
}
