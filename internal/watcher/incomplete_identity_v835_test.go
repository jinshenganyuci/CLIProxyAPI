package watcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestV835IncompleteProviderReloadPreservesIdentityConflictOwners(t *testing.T) {
	dir := t.TempDir()
	w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir, OAuthExcludedModels: map[string][]string{"codex": {"global-model"}}}, authQueue: make(chan AuthUpdate, 16)}
	write := func(name, namespace string) {
		t.Helper()
		data, errMarshal := json.Marshal(map[string]any{"type": "codex", "access_token": "synthetic", "proxy_url": "direct", "concurrency_limit": 3, "excluded_models": []string{"own-model"}, "codex_identity_version": 1, "codex_identity_namespace": namespace})
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if errWrite := os.WriteFile(filepath.Join(dir, name), data, 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	const namespace = "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"
	write("a.json", namespace)
	w.addOrUpdateClient(filepath.Join(dir, "a.json"))
	write("b.json", namespace)
	w.addOrUpdateClient(filepath.Join(dir, "b.json"))
	if w.authRevisions["a.json"] == w.authSourceRevisions["a.json"] {
		t.Fatal("fixture did not create an independent derived identity-conflict revision")
	}
	if errWrite := os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"type":`), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	w.reloadClients(false, []string{"codex"}, false)
	for _, id := range []string{"a.json", "b.json"} {
		auth := w.currentAuths[id]
		if auth == nil || auth.ProxyURL != "direct" || auth.Attributes[codex.CredentialIdentityConflictAttribute] != "true" || auth.Attributes["excluded_models"] != "own-model" {
			t.Fatalf("incomplete provider reload lost credential controls or conflict ownership for %s", id)
		}
		if update := w.pendingUpdates[id]; update.Action == AuthUpdateActionDelete {
			t.Fatalf("incomplete file caused deletion of %s", id)
		}
	}
	write("a.json", "6f3c61a1-f5bd-4265-93ed-a28612867322")
	w.addOrUpdateClient(filepath.Join(dir, "a.json"))
	for _, id := range []string{"a.json", "b.json"} {
		if w.currentAuths[id] == nil || w.currentAuths[id].Attributes[codex.CredentialIdentityConflictAttribute] != "" {
			t.Fatalf("completed file update did not clear resolved identity conflict for %s", id)
		}
	}
}
