package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestAuthSnapshotPreservesConcurrentCodexIdentity(t *testing.T) {
	const originalNamespace = "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"
	const finalNamespace = "6f3c61a1-f5bd-4265-93ed-a28612867322"
	for _, testCase := range []struct {
		name        string
		initialized bool
		plugin      bool
	}{
		{name: "initialization"},
		{name: "rotation", initialized: true},
		{name: "plugin_initialization", plugin: true},
		{name: "plugin_rotation", initialized: true, plugin: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			authDir := t.TempDir()
			fileName := "codex-snapshot.json"
			path := filepath.Join(authDir, fileName)
			metadata := map[string]any{"type": "codex", "access_token": "old-test-token"}
			if testCase.initialized {
				metadata[codex.CredentialIdentityVersionMetadataKey] = 1
				metadata[codex.CredentialIdentityNamespaceMetadataKey] = originalNamespace
			}
			writeMetadata := func() {
				t.Helper()
				raw, errMarshal := json.Marshal(metadata)
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
			}
			writeMetadata()
			watcher := &Watcher{
				authDir:   authDir,
				config:    &config.Config{AuthDir: authDir},
				authQueue: make(chan AuthUpdate, 8),
			}
			if testCase.plugin {
				watcher.pluginAuthParser = persistedAuthParserFunc(func(_ context.Context, request pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
					var parsed map[string]any
					if errParse := json.Unmarshal(request.RawJSON, &parsed); errParse != nil {
						return nil, true, errParse
					}
					delete(parsed, codex.CredentialIdentityVersionMetadataKey)
					delete(parsed, codex.CredentialIdentityNamespaceMetadataKey)
					parsed["plugin_owned"] = true
					return &coreauth.Auth{ID: request.FileName, Provider: request.Provider, Metadata: parsed}, true, nil
				})
			}
			if !watcher.addOrUpdateClient(path) {
				t.Fatal("initial file load failed")
			}
			finishScan := pauseAuthSnapshot(t, watcher, true)
			metadata["access_token"] = "new-test-token"
			metadata[codex.CredentialIdentityVersionMetadataKey] = 1
			metadata[codex.CredentialIdentityNamespaceMetadataKey] = finalNamespace
			writeMetadata()
			if !watcher.DispatchPersistedAuthUpdate(AuthUpdate{
				Action: AuthUpdateActionModify,
				ID:     fileName,
				Auth: &coreauth.Auth{
					ID: fileName, Provider: "codex",
					Attributes: map[string]string{coreauth.AttributePath: path},
				},
			}) {
				t.Fatal("persisted file dispatch failed")
			}
			finishScan()
			for source, auth := range map[string]*coreauth.Auth{
				"runtime": watcher.currentAuths[fileName],
				"queued":  watcher.pendingUpdates[fileName].Auth,
			} {
				if auth == nil {
					t.Fatalf("%s auth is missing", source)
				}
				namespace, _, errIdentity := codex.ParseCredentialIdentity(auth.Metadata)
				if errIdentity != nil || namespace.String() != finalNamespace {
					t.Fatalf("%s identity lost after stale scan: namespace=%s error=%v", source, namespace, errIdentity)
				}
				if auth.Metadata["access_token"] != "new-test-token" {
					t.Fatalf("%s token was replaced by stale scan", source)
				}
				if testCase.plugin && auth.Metadata["plugin_owned"] != true {
					t.Fatalf("%s plugin metadata is missing", source)
				}
			}
			if watcher.pendingUpdates[fileName].revision != watcher.authRevisions[fileName] {
				t.Fatal("queued update does not match latest auth revision")
			}
		})
	}
}
