package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestDispatchPersistedAuthUpdateWithRevisionRenamedPluginAuth(t *testing.T) {
	for _, withQueue := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue_%t", withQueue), func(t *testing.T) {
			const canonicalID = "plugin-renamed-codex"
			w, update := newPersistedPluginRevisionWatcher(t, []string{canonicalID}, withQueue)
			original := update.Auth
			ok, revision := w.DispatchPersistedAuthUpdateWithRevision(&update)
			if ok != withQueue || revision == 0 || revision != update.Revision() {
				t.Fatalf("renamed dispatch = %t/%d, update revision = %d, queue = %t", ok, revision, update.Revision(), withQueue)
			}
			if update.ID != canonicalID || update.Auth == original {
				t.Fatal("renamed dispatch retained the pre-serialization auth")
			}
			assertPersistedPluginRevisionAuth(t, update.Auth, canonicalID)
			assertPersistedPluginRevisionState(t, w, []string{canonicalID}, original.ID, withQueue)
			if revision != w.authRevisions[canonicalID] {
				t.Fatalf("synchronous revision = %d, want canonical revision %d", revision, w.authRevisions[canonicalID])
			}
		})
	}
}

func TestDispatchPersistedAuthUpdateWithRevisionQueuesPluginVirtualBatch(t *testing.T) {
	ids := []string{"plugin-codex-project-a", "plugin-codex-project-b"}
	w, update := newPersistedPluginRevisionWatcher(t, ids, true)
	originalID := update.Auth.ID
	update.SetRevision(99)
	ok, revision := w.DispatchPersistedAuthUpdateWithRevision(&update)
	if !ok || revision != 0 {
		t.Fatalf("virtual batch dispatch = %t/%d, want queued without synchronous revision", ok, revision)
	}
	if update.Auth != nil || update.ID != "" || update.Revision() != 0 {
		t.Fatal("queued-only result retained a pre-serialization auth or revision")
	}
	assertPersistedPluginRevisionState(t, w, ids, originalID, true)
	for _, id := range ids {
		if !coreauth.IsPluginVirtualAuth(w.currentAuths[id]) {
			t.Fatalf("canonical auth %s is missing the virtual source marker", id)
		}
	}
}

func TestDispatchPersistedAuthUpdateWithRevisionRejectsUnqueuedPluginVirtualBatch(t *testing.T) {
	ids := []string{"plugin-codex-project-a", "plugin-codex-project-b"}
	w, update := newPersistedPluginRevisionWatcher(t, ids, false)
	originalID := update.Auth.ID
	ok, revision := w.DispatchPersistedAuthUpdateWithRevision(&update)
	if ok || revision != 0 {
		t.Fatalf("virtual batch without a queue = %t/%d, want false/0", ok, revision)
	}
	assertPersistedPluginRevisionState(t, w, ids, originalID, false)
}

func TestDispatchPersistedAuthUpdateWithRevisionRetriesPluginVirtualBatchAfterQueueAttached(t *testing.T) {
	ids := []string{"plugin-codex-project-a", "plugin-codex-project-b"}
	w, update := newPersistedPluginRevisionWatcher(t, ids, false)
	originalID := update.Auth.ID
	if ok, revision := w.DispatchPersistedAuthUpdateWithRevision(&update); ok || revision != 0 {
		t.Fatalf("initial virtual batch without a queue = %t/%d, want false/0", ok, revision)
	}
	initialRevisions := maps.Clone(w.authRevisions)
	w.authQueue = make(chan AuthUpdate, len(ids))
	ok, revision := w.DispatchPersistedAuthUpdateWithRevision(&update)
	if !ok || revision != 0 {
		t.Fatalf("retried virtual batch = %t/%d, want queued without synchronous revision", ok, revision)
	}
	if update.Auth != nil || update.ID != "" || update.Revision() != 0 {
		t.Fatal("queued retry retained the original incomplete auth or revision")
	}
	assertPersistedPluginRevisionState(t, w, ids, originalID, true)
	if !maps.Equal(w.authRevisions, initialRevisions) {
		t.Fatal("retrying the unchanged file unexpectedly advanced source revisions")
	}
}

func newPersistedPluginRevisionWatcher(t *testing.T, ids []string, withQueue bool) (*Watcher, AuthUpdate) {
	t.Helper()
	authDir := t.TempDir()
	fileName := "codex-plugin-source.json"
	path := filepath.Join(authDir, fileName)
	raw := []byte(`{"type":"codex","access_token":"canonical-access","refresh_token":"canonical-refresh","proxy_url":"socks5://selected.example:1080","codex_identity_version":1,"codex_identity_namespace":"981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"}`)
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatalf("write canonical plugin source: %v", errWrite)
	}
	w := &Watcher{
		authDir: authDir,
		config:  &config.Config{},
		pluginAuthParser: persistedPluginRevisionParserFunc(func(_ context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
			if req.Path != path || req.FileName != fileName || req.Provider != "codex" {
				t.Fatalf("plugin parse request has the wrong source: %#v", req)
			}
			var metadata map[string]any
			if errParse := json.Unmarshal(req.RawJSON, &metadata); errParse != nil {
				return nil, false, errParse
			}
			// The synthesizer must restore identity from the persisted source even
			// when a plugin does not understand the fork's identity metadata.
			delete(metadata, codex.CredentialIdentityVersionMetadataKey)
			delete(metadata, codex.CredentialIdentityNamespaceMetadataKey)
			auths := make([]*coreauth.Auth, 0, len(ids))
			for _, id := range ids {
				parsed := maps.Clone(metadata)
				parsed["plugin_owned"] = id
				auths = append(auths, &coreauth.Auth{ID: id, Provider: "codex", Metadata: parsed})
			}
			return auths, true, nil
		}),
	}
	if withQueue {
		w.authQueue = make(chan AuthUpdate, len(ids))
	}
	update := AuthUpdate{
		Action: AuthUpdateActionModify,
		ID:     fileName,
		Auth: &coreauth.Auth{
			ID:       fileName,
			Provider: "codex",
			ProxyURL: "direct",
			Metadata: map[string]any{"access_token": "incomplete-before-save"},
			Attributes: map[string]string{
				coreauth.AttributePath: path,
			},
		},
	}
	return w, update
}

func assertPersistedPluginRevisionState(t *testing.T, w *Watcher, ids []string, originalID string, withQueue bool) {
	t.Helper()
	if _, exists := w.currentAuths[originalID]; exists {
		t.Fatal("watcher registered the original filename auth instead of plugin records")
	}
	if _, exists := w.pendingUpdates[originalID]; exists {
		t.Fatal("watcher queued the original filename auth instead of plugin records")
	}
	if _, exists := w.runtimeAuths[originalID]; exists {
		t.Fatal("watcher retained the original filename as a runtime override")
	}
	if len(w.currentAuths) != len(ids) {
		t.Fatalf("canonical auth count = %d, want %d", len(w.currentAuths), len(ids))
	}
	wantPending := 0
	if withQueue {
		wantPending = len(ids)
	}
	if len(w.pendingUpdates) != wantPending {
		t.Fatalf("pending auth count = %d, want %d", len(w.pendingUpdates), wantPending)
	}
	for _, id := range ids {
		assertPersistedPluginRevisionAuth(t, w.currentAuths[id], id)
		if w.authRevisions[id] == 0 {
			t.Fatalf("canonical auth %s has no source revision", id)
		}
		if withQueue {
			pending := w.pendingUpdates[id]
			assertPersistedPluginRevisionAuth(t, pending.Auth, id)
			if pending.ID != id || pending.Revision() != w.authRevisions[id] {
				t.Fatalf("pending auth %s revision = %d, want %d", pending.ID, pending.Revision(), w.authRevisions[id])
			}
		}
	}
}

func assertPersistedPluginRevisionAuth(t *testing.T, auth *coreauth.Auth, id string) {
	t.Helper()
	if auth == nil || auth.ID != id {
		t.Fatalf("canonical plugin auth for %s is missing", id)
	}
	if auth.Metadata["access_token"] != "canonical-access" || auth.Metadata["refresh_token"] != "canonical-refresh" || auth.Metadata["plugin_owned"] != id {
		t.Fatalf("canonical plugin auth %s lost persisted or plugin metadata", id)
	}
	if auth.ProxyURL != "socks5://selected.example:1080" {
		t.Fatalf("canonical plugin auth %s lost the selected proxy", id)
	}
	namespace, _, errIdentity := codex.ParseCredentialIdentity(auth.Metadata)
	if errIdentity != nil || namespace.String() != "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7" {
		t.Fatalf("canonical plugin auth %s lost persisted identity: %s, %v", id, namespace, errIdentity)
	}
}

type persistedPluginRevisionParserFunc func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error)

func (f persistedPluginRevisionParserFunc) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	auths, handled, errParse := f(ctx, req)
	if len(auths) == 0 {
		return nil, handled, errParse
	}
	return auths[0], handled, errParse
}

func (f persistedPluginRevisionParserFunc) ParseAuths(ctx context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	return f(ctx, req)
}

var _ synthesizer.PluginAuthParser = persistedPluginRevisionParserFunc(nil)
var _ synthesizer.PluginMultiAuthParser = persistedPluginRevisionParserFunc(nil)
