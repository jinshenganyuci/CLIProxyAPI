package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/credentialfile"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestDispatchPersistedAuthUpdateReloadsCanonicalFileAuth(t *testing.T) {
	for _, preloadFileEvent := range []bool{false, true} {
		name := "persist hook before file event"
		if preloadFileEvent {
			name = "file event before persist hook"
		}
		t.Run(name, func(t *testing.T) {
			authDir := t.TempDir()
			fileName := "codex-diagnostic.json"
			path := filepath.Join(authDir, fileName)
			raw := []byte(`{"type":"codex","email":"diagnostic@example.test","access_token":"token","codex_identity_version":1,"codex_identity_namespace":"981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"}`)
			if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
				t.Fatalf("write auth file: %v", errWrite)
			}

			updates := make(chan AuthUpdate, 4)
			w := &Watcher{
				authDir:         authDir,
				config:          &config.Config{},
				lastAuthHashes:  make(map[string]string),
				fileAuthsByPath: make(map[string]map[string]*coreauth.Auth),
				currentAuths:    make(map[string]*coreauth.Auth),
				authQueue:       updates,
				pluginAuthParser: persistedAuthParserFunc(func(_ context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
					return &coreauth.Auth{
						ID:       req.FileName,
						Provider: req.Provider,
						Metadata: map[string]any{
							"type":         req.Provider,
							"email":        "diagnostic@example.test",
							"access_token": "token",
							"plugin_owned": true,
						},
					}, true, nil
				}),
			}

			if preloadFileEvent {
				w.addOrUpdateClient(path)
			}

			preSerialization := &coreauth.Auth{
				ID:       fileName,
				Provider: "codex",
				Metadata: map[string]any{"email": "diagnostic@example.test"},
				Attributes: map[string]string{
					coreauth.AttributePath:   path,
					coreauth.AttributeSource: path,
				},
			}
			update := AuthUpdate{Action: AuthUpdateActionModify, ID: fileName, Auth: preSerialization}
			if ok, rev := w.DispatchPersistedAuthUpdateWithRevision(&update); !ok || rev == 0 || rev != update.Revision() {
				t.Fatalf("persisted canonical update = %v/%d, revision=%d", ok, rev, update.Revision())
			}
			if update.Auth == preSerialization || update.Auth.Metadata["access_token"] != "token" || update.Auth.Metadata["plugin_owned"] != true {
				t.Fatal("synchronous update did not use the canonical plugin-parsed auth")
			}
			if _, _, errIdentity := codex.ParseCredentialIdentity(update.Auth.Metadata); errIdentity != nil {
				t.Fatalf("synchronous canonical identity invalid: %v", errIdentity)
			}

			w.clientsMutex.RLock()
			current := w.currentAuths[fileName]
			w.clientsMutex.RUnlock()
			if current == nil {
				t.Fatal("canonical runtime auth is missing")
			}
			if _, _, errIdentity := codex.ParseCredentialIdentity(current.Metadata); errIdentity != nil {
				t.Fatalf("runtime identity invalid: %v", errIdentity)
			}
			if got := current.Metadata["plugin_owned"]; got != true {
				t.Fatalf("plugin metadata = %#v, want true", got)
			}

			w.dispatchMu.Lock()
			pending := w.pendingUpdates[fileName]
			w.dispatchMu.Unlock()
			if pending.Auth == nil {
				t.Fatal("canonical runtime update was not queued")
			}
			if pending.Revision() != update.Revision() {
				t.Fatalf("queued revision=%d differs from synchronous revision=%d", pending.Revision(), update.Revision())
			}
			if _, _, errIdentity := codex.ParseCredentialIdentity(pending.Auth.Metadata); errIdentity != nil {
				t.Fatalf("queued runtime identity invalid: %v", errIdentity)
			}
			if got := pending.Auth.Metadata["plugin_owned"]; got != true {
				t.Fatalf("queued plugin metadata = %#v, want true", got)
			}
		})
	}
}

func TestDispatchPersistedAuthUpdateKeepsRuntimeStateOnCanonicalParseFailure(t *testing.T) {
	authDir := t.TempDir()
	fileName := "codex-diagnostic.json"
	path := filepath.Join(authDir, fileName)
	raw := []byte(`{"type":"codex","email":"diagnostic@example.test","access_token":"token","weight":1.5,"codex_identity_version":1,"codex_identity_namespace":"981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"}`)
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	existing := &coreauth.Auth{
		ID:       fileName,
		Provider: "codex",
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "existing-token",
			codex.CredentialIdentityVersionMetadataKey:   float64(codex.CredentialIdentityCurrentVersion),
			codex.CredentialIdentityNamespaceMetadataKey: "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7",
		},
	}
	updates := make(chan AuthUpdate, 1)
	w := &Watcher{
		authDir:         authDir,
		config:          &config.Config{},
		lastAuthHashes:  make(map[string]string),
		fileAuthsByPath: map[string]map[string]*coreauth.Auth{filepath.Clean(path): {fileName: nil}},
		currentAuths:    map[string]*coreauth.Auth{fileName: existing.Clone()},
		authQueue:       updates,
	}

	preSerialization := &coreauth.Auth{
		ID:       fileName,
		Provider: "codex",
		Metadata: map[string]any{"email": "diagnostic@example.test"},
		Attributes: map[string]string{
			coreauth.AttributePath:   path,
			coreauth.AttributeSource: path,
		},
	}
	if ok := w.DispatchPersistedAuthUpdate(AuthUpdate{
		Action: AuthUpdateActionModify,
		ID:     fileName,
		Auth:   preSerialization,
	}); ok {
		t.Fatal("DispatchPersistedAuthUpdate() = true, want false")
	}

	w.clientsMutex.RLock()
	current := w.currentAuths[fileName]
	_, hashCached := w.lastAuthHashes[filepath.Clean(path)]
	w.clientsMutex.RUnlock()
	if current == nil {
		t.Fatal("existing runtime auth was removed")
	}
	if _, _, errIdentity := codex.ParseCredentialIdentity(current.Metadata); errIdentity != nil {
		t.Fatalf("existing runtime identity changed: %v", errIdentity)
	}
	if hashCached {
		t.Fatal("failed canonical parse cached the new file hash")
	}
	w.dispatchMu.Lock()
	pendingCount := len(w.pendingUpdates)
	w.dispatchMu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("queued updates after parse failure = %d, want 0", pendingCount)
	}
}

func TestDispatchPersistedAuthUpdateWaitsForCredentialWrite(t *testing.T) {
	for _, valid := range []bool{true, false} {
		name := "completed refresh"
		if !valid {
			name = "invalid completed write"
		}
		t.Run(name, func(t *testing.T) {
			authDir := t.TempDir()
			fileName := "codex-refresh.json"
			path := filepath.Join(authDir, fileName)
			w := &Watcher{
				authDir:      authDir,
				config:       &config.Config{},
				currentAuths: make(map[string]*coreauth.Auth),
				authQueue:    make(chan AuthUpdate, 1),
				pluginAuthParser: persistedAuthParserFunc(func(_ context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
					// The reader must release the file lock before plugin callbacks.
					unlock := credentialfile.Lock(req.Path)
					unlock()
					var metadata map[string]any
					if errParse := json.Unmarshal(req.RawJSON, &metadata); errParse != nil {
						return nil, false, errParse
					}
					return &coreauth.Auth{ID: req.FileName, Provider: req.Provider, Metadata: metadata}, true, nil
				}),
			}
			update := AuthUpdate{
				Action: AuthUpdateActionAdd,
				ID:     fileName,
				Auth: &coreauth.Auth{
					ID:         fileName,
					Provider:   "codex",
					Metadata:   map[string]any{"access_token": "pre-serialization-token"},
					Attributes: map[string]string{coreauth.AttributePath: path},
				},
			}

			// Reproduce the truncate/write window used by refresh persistence.
			unlock := credentialfile.Lock(path)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(unlock) }
			defer release()
			if errWrite := os.WriteFile(path, nil, 0o600); errWrite != nil {
				t.Fatalf("truncate credential: %v", errWrite)
			}
			completed := make(chan bool, 1)
			go func() { completed <- w.DispatchPersistedAuthUpdate(update) }()
			waitForPersistedCredentialReadLock(t, completed)

			raw := []byte(`{"type":"codex","access_token":"refreshed-access","refresh_token":"refreshed-refresh","proxy_url":"socks5://selected.example:1080","codex_identity_version":1,"codex_identity_namespace":"981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"}`)
			if !valid {
				raw = []byte(`{"type":`)
			}
			if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
				t.Fatalf("complete credential write: %v", errWrite)
			}
			release()
			select {
			case ok := <-completed:
				if ok != valid {
					t.Fatalf("DispatchPersistedAuthUpdate() = %v, want %v", ok, valid)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canonical reload remained blocked after credential write completed")
			}
			if !valid {
				if len(w.currentAuths) != 0 || len(w.pendingUpdates) != 0 {
					t.Fatal("invalid completed write published an auth update")
				}
				return
			}
			current := w.currentAuths[fileName]
			if current == nil || current.Metadata["access_token"] != "refreshed-access" || current.Metadata["refresh_token"] != "refreshed-refresh" {
				t.Fatal("canonical reload did not publish the completed refresh tokens")
			}
			if current.ProxyURL != "socks5://selected.example:1080" {
				t.Fatal("canonical reload lost the selected proxy")
			}
			if _, _, errIdentity := codex.ParseCredentialIdentity(current.Metadata); errIdentity != nil {
				t.Fatalf("canonical reload lost credential identity: %v", errIdentity)
			}
			if pending := w.pendingUpdates[fileName]; pending.Auth == nil || pending.Auth.Metadata["access_token"] != "refreshed-access" {
				t.Fatal("completed canonical refresh was not queued")
			}
		})
	}
}

func waitForPersistedCredentialReadLock(t *testing.T, completed <-chan bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	stack := make([]byte, 128<<10)
	for {
		select {
		case ok := <-completed:
			t.Fatalf("canonical reload returned %v while credential persistence still held a truncated file", ok)
		case <-deadline:
			t.Fatal("canonical reload did not wait for the credential write lock")
		default:
		}
		// A mutex wait is not a durable block for testing/synctest. Observe the
		// blocked reader instead of using a delay to guess whether it has run.
		n := runtime.Stack(stack, true)
		for _, goroutine := range strings.Split(string(stack[:n]), "\n\n") {
			header, _, _ := strings.Cut(goroutine, "\n")
			if strings.Contains(header, "[sync.Mutex.Lock") &&
				strings.Contains(goroutine, "credentialfile.Lock(") &&
				strings.Contains(goroutine, "(*Watcher).addOrUpdateClientLocked(") {
				return
			}
		}
		runtime.Gosched()
	}
}

type persistedAuthParserFunc func(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error)

func (f persistedAuthParserFunc) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	return f(ctx, req)
}

var _ synthesizer.PluginAuthParser = persistedAuthParserFunc(nil)
