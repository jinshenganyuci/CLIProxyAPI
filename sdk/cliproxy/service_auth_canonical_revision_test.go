package cliproxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRuntimeAuthSyncHook_CanonicalFileAndRevision(t *testing.T) {
	for _, withQueue := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue_%t", withQueue), func(t *testing.T) {
			authDir := t.TempDir()
			authID := "codex-canonical-revision.json"
			path := filepath.Join(authDir, authID)
			const namespace = "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"
			const proxyURL = "http://127.0.0.1:18522"
			writeCredential := func(token string) {
				t.Helper()
				data := fmt.Sprintf(`{"type":"codex","email":"revision@example.test","access_token":%q,"proxy_url":%q,"codex_identity_version":1,"codex_identity_namespace":%q}`, token, proxyURL, namespace)
				if errWrite := os.WriteFile(path, []byte(data), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
			}
			writeCredential("canonical-first")
			cfg := &config.Config{AuthDir: authDir}
			wrapper, errWatcher := defaultWatcherFactory("", authDir, nil)
			if errWatcher != nil {
				t.Fatal(errWatcher)
			}
			t.Cleanup(func() {
				if errStop := wrapper.Stop(); errStop != nil {
					t.Errorf("stop watcher: %v", errStop)
				}
			})
			wrapper.SetConfig(cfg)
			if withQueue {
				wrapper.SetAuthUpdateQueue(make(chan watcher.AuthUpdate, 8))
			}
			manager := coreauth.NewManager(nil, nil, nil)
			service := &Service{cfg: cfg, coreManager: manager, watcher: wrapper}
			t.Cleanup(func() { internalregistry.GetGlobalRegistry().UnregisterClient(authID) })
			beforeSerialization := &coreauth.Auth{
				ID: authID, Provider: "codex", ProxyURL: "direct",
				Metadata:   map[string]any{"access_token": "stale-before-save"},
				Attributes: map[string]string{coreauth.AttributePath: path},
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			hook := service.runtimeAuthSyncHook()
			assertCurrent := func(token string) *coreauth.Auth {
				t.Helper()
				current, exists := manager.GetByID(authID)
				if !exists || current.Metadata["access_token"] != token || current.ProxyURL != proxyURL || current.Metadata["codex_identity_namespace"] != namespace {
					t.Fatalf("runtime did not preserve canonical token, proxy and namespace: %#v", current)
				}
				return current
			}
			if errSync := hook(ctx, beforeSerialization); errSync != nil {
				t.Fatal(errSync)
			}
			first := assertCurrent("canonical-first")
			firstRevision := service.authRevisions[authID]
			if firstRevision == 0 {
				t.Fatal("synchronous canonical update has no watcher revision")
			}
			writeCredential("canonical-refreshed")
			if errSync := hook(ctx, beforeSerialization); errSync != nil {
				t.Fatal(errSync)
			}
			assertCurrent("canonical-refreshed")
			if service.authRevisions[authID] <= firstRevision {
				t.Fatal("new canonical update did not advance the revision")
			}
			stale := watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authID, Auth: first}
			stale.SetRevision(firstRevision)
			service.handleAuthUpdate(coreauth.WithSkipPersist(context.Background()), stale)
			assertCurrent("canonical-refreshed")
			if errWrite := os.WriteFile(path, []byte("{"), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if errSync := hook(ctx, beforeSerialization); errSync == nil {
				t.Fatal("invalid canonical file unexpectedly accepted the stale input auth")
			}
			assertCurrent("canonical-refreshed")
		})
	}
}
