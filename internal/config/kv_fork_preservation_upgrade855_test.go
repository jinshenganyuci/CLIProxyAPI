package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestV855KVSavePreservesForkCredentialControls(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled_%t", disabled), func(t *testing.T) {
			const source = `config-version: 8
oauth:
  auth-dir: /synthetic/auth
  providers:
    codex: {credential-proxy-policy: require, credential-identity: {enabled: true}}
api-keys:
  gemini:
    - name: retained-credential
      keys:
        - {api-key: synthetic-key, proxy-url: direct, concurrency-limit: 3, connection-pool: {enabled: true}}
server:
  kv-store: {disabled: false, path: ""}
`
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, []byte(source), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			for round, kvPath := range []string{"", "/synthetic/explicit.db", ""} {
				cfg, errLoad := LoadConfig(path)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				cfg.KVStore = KVStoreConfig{Disabled: disabled, Path: kvPath}
				cfg.AuthDir = fmt.Sprintf("/synthetic/auth-%d", round)
				if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
					t.Fatal(errSave)
				}
				reloaded, errLoad := LoadConfig(path)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				if reloaded.KVStore != cfg.KVStore || reloaded.AuthDir != cfg.AuthDir {
					t.Fatalf("round %d changed KV or auth-dir settings", round)
				}
				wantPath := kvPath
				if wantPath == "" {
					wantPath = filepath.Join(cfg.AuthDir, "kv.db")
				}
				if reloaded.EffectiveKVStorePath() != wantPath {
					t.Fatalf("round %d resolved wrong KV path", round)
				}
				if reloaded.Codex.CredentialProxyPolicy != "require" || !reloaded.Codex.CredentialIdentity.Enabled || len(reloaded.GeminiKey) != 1 {
					t.Fatal("KV save changed OAuth identity, proxy policy, or credential count")
				}
				key := reloaded.GeminiKey[0]
				if key.ProxyURL != "direct" || key.ConcurrencyLimit == nil || *key.ConcurrencyLimit != 3 || key.ConnectionPool == nil || key.ConnectionPool.Enabled == nil || !*key.ConnectionPool.Enabled {
					t.Fatal("KV save changed credential proxy, concurrency, or pool")
				}
			}
		})
	}
}
