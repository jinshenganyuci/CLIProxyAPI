package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
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
			if ok := w.DispatchPersistedAuthUpdate(AuthUpdate{
				Action: AuthUpdateActionModify,
				ID:     fileName,
				Auth:   preSerialization,
			}); !ok {
				t.Fatal("DispatchPersistedAuthUpdate() = false")
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

type persistedAuthParserFunc func(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error)

func (f persistedAuthParserFunc) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	return f(ctx, req)
}

var _ synthesizer.PluginAuthParser = persistedAuthParserFunc(nil)
