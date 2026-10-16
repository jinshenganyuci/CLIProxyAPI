package cliproxy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Exercise real fsnotify removals so the watcher's file hash cache is invalidated
// before a management save recreates identical bytes and publishes inline.
func TestV830FileRecreationRejectsDelayedRemovalAfterCanonicalSave(t *testing.T) {
	for _, sameBytes := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_bytes=%t", sameBytes), func(t *testing.T) {
			dir := t.TempDir()
			authDir := filepath.Join(dir, "auths")
			if err := os.Mkdir(authDir, 0o700); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte("auth-dir: "+authDir+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			id := "same-byte-recreated.json"
			path := filepath.Join(authDir, id)
			const namespace = "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"
			data := []byte(fmt.Sprintf(`{"type":"codex","access_token":"canonical-first","proxy_url":"direct","concurrency_limit":3,"codex_identity_version":1,"codex_identity_namespace":%q}`, namespace))
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{AuthDir: authDir}
			w, err := defaultWatcherFactory(configPath, authDir, nil)
			if err != nil {
				t.Fatal(err)
			}
			w.SetConfig(cfg)
			queue := make(chan watcher.AuthUpdate, 16)
			w.SetAuthUpdateQueue(queue)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() {
				cancel()
				if errStop := w.Stop(); errStop != nil {
					t.Error(errStop)
				}
			})
			manager := coreauth.NewManager(nil, nil, nil)
			service := &Service{cfg: cfg, coreManager: manager, watcher: w}
			reg := GlobalModelRegistry()
			t.Cleanup(func() { reg.UnregisterClient(id) })
			next := func(action watcher.AuthUpdateAction) watcher.AuthUpdate {
				t.Helper()
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				for {
					select {
					case update := <-queue:
						if update.ID == id && update.Action == action {
							return update
						}
					case <-timer.C:
						t.Fatalf("missing file event %v", action)
						return watcher.AuthUpdate{}
					}
				}
			}
			if errStart := w.Start(ctx); errStart != nil {
				t.Fatal(errStart)
			}
			initial := next(watcher.AuthUpdateActionAdd)
			service.handleAuthUpdate(coreauth.WithSkipPersist(ctx), initial)
			if errRemove := os.Remove(path); errRemove != nil {
				t.Fatal(errRemove)
			}
			deletion := next(watcher.AuthUpdateActionDelete)
			if !deletion.IsFileRemoval() || deletion.Revision() <= initial.Revision() {
				t.Fatalf("invalid removal: %+v", deletion)
			}
			wantToken := "canonical-first"
			if !sameBytes {
				wantToken = "canonical-second"
				data = bytes.ReplaceAll(data, []byte("canonical-first"), []byte(wantToken))
			}
			if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			snapshot, errSnapshot := w.SnapshotAuthFile(path)
			if errSnapshot != nil || len(snapshot) != 1 {
				t.Fatalf("canonical snapshot: %+v %v", snapshot, errSnapshot)
			}
			if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), snapshot[0]); errRegister != nil {
				t.Fatal(errRegister)
			}
			input := snapshot[0].Clone()
			input.Metadata["access_token"] = "stale-before-serialization"
			input.ProxyURL = "http://stale.invalid:1"
			if errSync := service.runtimeAuthSyncHook()(ctx, input); errSync != nil {
				t.Fatal(errSync)
			}
			service.handleAuthUpdate(coreauth.WithSkipPersist(ctx), deletion)
			current, present := manager.GetByID(id)
			if !present || current.Metadata["access_token"] != wantToken || current.ProxyURL != "direct" || current.Metadata["codex_identity_namespace"] != namespace || coreauth.EffectiveConcurrencyLimit(current) != 3 {
				t.Fatalf("delayed removal or stale hook damaged canonical auth: %+v", current)
			}
			if len(reg.GetModelsForClient(id)) == 0 {
				t.Fatal("delayed removal removed recreated models")
			}
			persisted, errRead := os.ReadFile(path)
			if errRead != nil || !bytes.Equal(persisted, data) {
				t.Fatalf("canonical persisted bytes changed: %v", errRead)
			}
			if service.authRevisions[id] <= deletion.Revision() {
				t.Fatal("canonical save did not supersede deletion revision")
			}
		})
	}
}
