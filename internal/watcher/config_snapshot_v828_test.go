package watcher

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV828WatcherSynthesisOwnsCredentialControlSnapshot(t *testing.T) {
	limit := 3
	cfg := &config.Config{AuthDir: t.TempDir(), CodexKey: []config.CodexKey{
		{APIKey: "synthetic-limited", BaseURL: "https://codex.invalid", ProxyURL: "socks5h://proxy.invalid:1080", ConcurrencyLimit: &limit, Headers: map[string]string{"X-Synthetic": "original"}},
		{APIKey: "synthetic-default", BaseURL: "https://codex.invalid", ProxyURL: "direct"},
	}}
	w := &Watcher{authDir: cfg.AuthDir}
	w.SetConfig(cfg)
	before := w.SnapshotCoreAuths()
	if len(before) != 2 {
		t.Fatalf("got %d synthesized credentials", len(before))
	}
	cfg.CodexKey[0].ProxyURL = "direct"
	*cfg.CodexKey[0].ConcurrencyLimit = 99
	cfg.CodexKey[0].Headers["X-Synthetic"] = "mutated"
	cfg.CodexKey[1].APIKey = "synthetic-replaced"
	after := w.SnapshotCoreAuths()
	if len(after) != 2 {
		t.Fatal("caller mutation changed the credential count")
	}
	previous := make(map[string]*coreauth.Auth)
	for _, auth := range before {
		previous[auth.ID] = auth
	}
	for _, auth := range after {
		original := previous[auth.ID]
		if original == nil || auth.ProxyURL != original.ProxyURL || auth.Attributes["header:X-Synthetic"] != original.Attributes["header:X-Synthetic"] || coreauth.EffectiveConcurrencyLimit(auth) != coreauth.EffectiveConcurrencyLimit(original) {
			t.Fatal("watcher synthesis changed identity, proxy, concurrency or headers after caller mutation")
		}
		want := 10
		if auth.ProxyURL != "direct" {
			want = 3
		}
		if coreauth.EffectiveConcurrencyLimit(auth) != want {
			t.Fatalf("concurrency limit = %d, want %d", coreauth.EffectiveConcurrencyLimit(auth), want)
		}
	}
}
