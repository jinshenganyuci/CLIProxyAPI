package watcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV836ReadErrorsRetainIdentityOwnersButDeletionRemovesThem(t *testing.T) {
	for _, mode := range []string{"refresh", "rescan", "provider-reload"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 16)}
			data, errMarshal := json.Marshal(map[string]any{"type": "codex", "access_token": "synthetic", "proxy_url": "direct", "concurrency_limit": 3, "codex_identity_version": 1, "codex_identity_namespace": "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			for _, name := range []string{"a.json", "b.json"} {
				path := filepath.Join(dir, name)
				if errWrite := os.WriteFile(path, data, 0600); errWrite != nil {
					t.Fatal(errWrite)
				}
				w.addOrUpdateClient(path)
			}
			path := filepath.Join(dir, "a.json")
			backup := filepath.Join(dir, "a.saved")
			if errRename := os.Rename(path, backup); errRename != nil {
				t.Fatal(errRename)
			}
			// A symlink loop produces a real non-ENOENT read error even as root.
			if errSymlink := os.Symlink("a.json", path); errSymlink != nil {
				t.Skipf("symlinks unavailable: %v", errSymlink)
			}
			if _, errRead := os.ReadFile(path); errRead == nil || os.IsNotExist(errRead) {
				t.Fatalf("fixture did not produce a non-ENOENT read error: %v", errRead)
			}
			scan := func() {
				switch mode {
				case "rescan":
					w.reloadClients(true, nil, false)
				case "provider-reload":
					w.reloadClients(false, []string{"codex"}, false)
				default:
					w.refreshAuthState(false)
				}
			}
			scan()
			for _, name := range []string{"a.json", "b.json"} {
				auth := w.currentAuths[name]
				if auth == nil || auth.ProxyURL != "direct" || coreauth.EffectiveConcurrencyLimit(auth) != 3 || auth.Attributes[codex.CredentialIdentityConflictAttribute] != "true" || w.pendingUpdates[name].Action == AuthUpdateActionDelete {
					t.Fatalf("read error removed fork controls or conflict ownership from %s", name)
				}
			}
			if errRemove := os.Remove(path); errRemove != nil {
				t.Fatal(errRemove)
			}
			scan()
			if w.currentAuths["a.json"] != nil || w.pendingUpdates["a.json"].Action != AuthUpdateActionDelete {
				t.Fatal("actual removal retained the unreadable credential")
			}
			if remaining := w.currentAuths["b.json"]; remaining == nil || remaining.Attributes[codex.CredentialIdentityConflictAttribute] != "" {
				t.Fatal("actual deletion did not release the surviving identity owner")
			}
		})
	}
}
