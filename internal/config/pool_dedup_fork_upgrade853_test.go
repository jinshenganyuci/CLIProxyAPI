package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestV853DistinctPoolsRetainCredentialLimitsAcrossSave(t *testing.T) {
	const source = `config-version: 8
oauth:
  providers:
    codex: {credential-proxy-policy: require, credential-identity: {enabled: true}}
upstream:
  claude:
    header-defaults: {native-entrypoints: [CLI, vscode, cli]}
api-keys:
  gemini:
    - name: same-credential-distinct-pools
      base-url: https://example.invalid
      keys:
        - {api-key: synthetic-key, proxy-url: direct, concurrency-limit: 2, connection-pool: {enabled: true}}
        - {api-key: synthetic-key, proxy-url: direct, concurrency-limit: 7, connection-pool: {enabled: false}}
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(source), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	for round := 0; round < 3; round++ {
		cfg, errLoad := LoadConfig(path)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		if len(cfg.GeminiKey) != 2 {
			t.Fatalf("round %d collapsed distinct pool credentials", round)
		}
		for i, limit := range []int{2, 7} {
			key := cfg.GeminiKey[i]
			if key.ConcurrencyLimit == nil || *key.ConcurrencyLimit != limit || key.ProxyURL != "direct" || key.ConnectionPool == nil || key.ConnectionPool.Enabled == nil || *key.ConnectionPool.Enabled != (i == 0) {
				t.Fatalf("round %d changed independent controls on key %d", round, i)
			}
		}
		if cfg.Codex.CredentialProxyPolicy != "require" || !cfg.Codex.CredentialIdentity.Enabled {
			t.Fatal("save changed OAuth identity or proxy policy")
		}
		entries := cfg.ClaudeHeaderDefaults.NativeEntrypoints
		if len(entries) != 2 || entries[0] != "cli" || entries[1] != "vscode" {
			t.Fatal("native entrypoint normalization or preservation changed")
		}
		cfg.RequestRetry = round + 1
		if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
			t.Fatal(errSave)
		}
	}
}
