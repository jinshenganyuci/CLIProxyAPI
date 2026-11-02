package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestV851PoolSavePreservesCredentialLimitsAndOAuthPolicy(t *testing.T) {
	const source = `config-version: 8
oauth:
  providers:
    codex:
      credential-proxy-policy: require
      credential-identity: {enabled: true}
client:
  claude: {show-codex-commentary: true}
api-keys:
  codex:
    - name: synthetic-codex
      base-url: https://example.invalid/v1
      connection-pool: {enabled: true, idle-conn-timeout: 60s, max-idle-conns-per-host: 3}
      keys:
        - {api-key: synthetic-a, proxy-url: direct, concurrency-limit: 4}
        - api-key: synthetic-b
          proxy-url: socks5h://127.0.0.1:1082
          concurrency-limit: 7
          connection-pool: {enabled: false}
  openai-compatibility:
    - name: synthetic-compat
      base-url: https://example.invalid/v1
      concurrency-limit: 9
      connection-pool: {enabled: true, idle-conn-timeout: 90s}
      keys:
        - {api-key: synthetic-c, proxy-url: direct, concurrency-limit: 2}
        - {api-key: synthetic-d, proxy-url: direct, concurrency-limit: null, connection-pool: {enabled: false}}
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
		if !cfg.Codex.CredentialIdentity.Enabled || cfg.Codex.CredentialProxyPolicy != "require" || !cfg.Client.Claude.ShowCodexCommentary {
			t.Fatalf("round %d lost OAuth identity, proxy policy, or commentary", round)
		}
		if len(cfg.CodexKey) != 2 || len(cfg.OpenAICompatibility) != 1 {
			t.Fatalf("round %d changed credential groups", round)
		}
		for i, expected := range []int{4, 7} {
			key := cfg.CodexKey[i]
			if key.ConcurrencyLimit == nil || *key.ConcurrencyLimit != expected || key.ConnectionPool == nil || key.ConnectionPool.Enabled == nil || *key.ConnectionPool.Enabled != (i == 0) || key.ConnectionPool.IdleConnTimeout != "60s" {
				t.Fatalf("round %d lost independent Codex limit/pool settings for key %d", round, i)
			}
		}
		if cfg.CodexKey[0].ProxyURL != "direct" || cfg.CodexKey[1].ProxyURL != "socks5h://127.0.0.1:1082" {
			t.Fatalf("round %d changed credential egress", round)
		}
		compat := cfg.OpenAICompatibility[0]
		if compat.ConcurrencyLimit == nil || *compat.ConcurrencyLimit != 9 || compat.ConnectionPool == nil || compat.ConnectionPool.Enabled == nil || !*compat.ConnectionPool.Enabled || len(compat.APIKeyEntries) != 2 {
			t.Fatalf("round %d lost compatibility group settings", round)
		}
		if compat.APIKeyEntries[0].ConcurrencyLimit == nil || *compat.APIKeyEntries[0].ConcurrencyLimit != 2 || compat.APIKeyEntries[1].ConcurrencyLimit != nil {
			t.Fatalf("round %d changed per-key limits or inherited the fallback limit", round)
		}
		if pool := compat.APIKeyEntries[1].ConnectionPool; pool == nil || pool.Enabled == nil || *pool.Enabled {
			t.Fatalf("round %d lost explicit per-key pool opt-out", round)
		}
		apiConfig := cfg.ForAPIKey()
		if apiConfig.Codex.CredentialIdentity.Enabled || apiConfig.Codex.CredentialProxyPolicy != "" || !apiConfig.Client.Claude.ShowCodexCommentary {
			t.Fatalf("round %d mixed OAuth-only policy with shared client behavior", round)
		}
		cfg.RequestRetry = round + 1
		if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
			t.Fatal(errSave)
		}
		saved, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errValidate := ValidateV8Config(saved); errValidate != nil {
			t.Fatalf("round %d saved invalid configuration: %v", round, errValidate)
		}
	}
}
