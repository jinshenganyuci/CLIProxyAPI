package watcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV830FileRemovalAndIdenticalRecreationPreserveCanonicalIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	raw := []byte(`{"type":"codex","access_token":"synthetic-token","proxy_url":"direct","concurrency_limit":3,"codex_identity_version":1,"codex_identity_namespace":"981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"}`)
	write := func() {
		t.Helper()
		if errWrite := os.WriteFile(path, raw, 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 8)}
	write()
	if !w.addOrUpdateClient(path) {
		t.Fatal("initial file was not synthesized")
	}
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatal(errRemove)
	}
	w.removeClient(path)
	deleted := w.pendingUpdates["account.json"]
	if !deleted.IsFileRemoval() || deleted.Revision() == 0 {
		t.Fatal("real file removal did not record a stamped deletion")
	}
	write()
	update := AuthUpdate{Action: AuthUpdateActionModify, ID: "account.json", Auth: &coreauth.Auth{ID: "account.json", Provider: "codex", Attributes: map[string]string{coreauth.AttributePath: path}}}
	queued, revision := w.DispatchPersistedAuthUpdateWithRevision(&update)
	if !queued || revision <= deleted.Revision() || update.Auth == nil || update.IsFileRemoval() {
		t.Fatal("identical recreation did not supersede the actual file-removal tombstone")
	}
	fileAuths, errSnapshot := w.SnapshotAuthFile(path)
	if errSnapshot != nil || len(fileAuths) != 1 {
		t.Fatalf("snapshot of recreated file: %d auths, %v", len(fileAuths), errSnapshot)
	}
	for _, auth := range []*coreauth.Auth{update.Auth, fileAuths[0], w.currentAuths["account.json"]} {
		if auth == nil || auth.Metadata["access_token"] != "synthetic-token" || auth.ProxyURL != "direct" {
			t.Fatal("recreation lost persisted credential content")
		}
		if _, _, errIdentity := codex.ParseCredentialIdentity(auth.Metadata); errIdentity != nil {
			t.Fatal("recreation lost the canonical credential identity")
		}
	}
}
