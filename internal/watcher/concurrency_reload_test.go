package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestConcurrencyOnlyConfigReloadPreservesIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	reloads := 0
	w := &Watcher{configPath: path, authDir: dir, reloadCallback: func(*config.Config) { reloads++ }}
	var previous *coreauth.Auth
	for _, limit := range []int{3, 1, 10} {
		body := fmt.Sprintf("auth-dir: %q\ncodex-api-key:\n  - api-key: test\n    base-url: http://example.invalid\n    concurrency-limit: %d\n", dir, limit)
		if errWrite := os.WriteFile(path, []byte(body), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		w.ReloadConfigIfChanged()
		auths := snapshotCoreAuths(w.config, dir, nil)
		if len(auths) != 1 || coreauth.EffectiveConcurrencyLimit(auths[0]) != limit {
			t.Fatalf("reload failed to apply concurrency limit %d: %v", limit, auths)
		}
		if previous != nil {
			if previous.ID != auths[0].ID || previous.EnsureIndex() != auths[0].EnsureIndex() {
				t.Fatal("concurrency-only edit changed credential identity")
			}
			if authEqual(previous, auths[0]) {
				t.Fatal("auth diff ignored concurrency-only edit")
			}
		}
		previous = auths[0]
	}
	if reloads != 3 {
		t.Fatalf("reload count = %d, want 3", reloads)
	}
	oldHash := w.lastConfigHash
	if errWrite := os.WriteFile(path, []byte("codex-api-key:\n  - api-key: test\n    base-url: http://example.invalid\n    concurrency-limit: 1.5\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	w.ReloadConfigIfChanged()
	if reloads != 3 || w.lastConfigHash != oldHash || *w.config.CodexKey[0].ConcurrencyLimit != 10 {
		t.Fatal("invalid reload replaced the valid concurrency configuration")
	}
}
