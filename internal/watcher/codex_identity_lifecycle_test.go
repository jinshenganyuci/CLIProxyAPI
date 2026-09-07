package watcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const watcherSharedIdentityNamespace = "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"
const watcherRotatedIdentityNamespace = "6f3c61a1-f5bd-4265-93ed-a28612867322"

func TestCodexIdentityConflictsFollowIncrementalFileLifecycle(t *testing.T) {
	for _, resolution := range []string{"remove", "rotate"} {
		t.Run(resolution, func(t *testing.T) {
			w := newCodexIdentityLifecycleWatcher(t)
			writeCodexIdentityLifecycleFile(t, w, "a.json", watcherSharedIdentityNamespace, "token-a")
			requireCodexIdentityConflict(t, w, "a.json", false)
			revisionA := w.authRevisions["a.json"]

			// A duplicate arriving while a scan is paused must mark both owners.
			finishScan := pauseAuthSnapshot(t, w, true)
			writeCodexIdentityLifecycleFile(t, w, "b.json", watcherSharedIdentityNamespace, "token-b")
			requireCodexIdentityConflict(t, w, "a.json", true)
			requireCodexIdentityConflict(t, w, "b.json", true)
			if w.authRevisions["a.json"] <= revisionA {
				t.Fatal("existing owner's conflict update was not revision stamped")
			}
			finishScan()
			requireCodexIdentityConflict(t, w, "a.json", true)
			requireCodexIdentityConflict(t, w, "b.json", true)

			// Refresh reconstructs auth from disk without the runtime-only marker.
			writeCodexIdentityLifecycleFile(t, w, "b.json", watcherSharedIdentityNamespace, "refreshed-token-b")
			requireCodexIdentityConflict(t, w, "a.json", true)
			requireCodexIdentityConflict(t, w, "b.json", true)
			if got := w.pendingUpdates["b.json"].Auth.Metadata["access_token"]; got != "refreshed-token-b" {
				t.Fatalf("queued refreshed token = %v", got)
			}

			finishScan = pauseAuthSnapshot(t, w, true)
			if resolution == "remove" {
				path := filepath.Join(w.authDir, "b.json")
				if errRemove := os.Remove(path); errRemove != nil {
					t.Fatal(errRemove)
				}
				w.removeClient(path)
			} else {
				writeCodexIdentityLifecycleFile(t, w, "b.json", watcherRotatedIdentityNamespace, "refreshed-token-b")
			}
			requireCodexIdentityConflict(t, w, "a.json", false)
			finishScan()
			requireCodexIdentityConflict(t, w, "a.json", false)
			if resolution == "remove" {
				pending := w.pendingUpdates["b.json"]
				if w.currentAuths["b.json"] != nil || pending.Action != AuthUpdateActionDelete || pending.revision != w.authRevisions["b.json"] {
					t.Fatal("stale scan resurrected removed duplicate")
				}
			} else {
				requireCodexIdentityConflict(t, w, "b.json", false)
				if got := w.currentAuths["b.json"].Metadata[codex.CredentialIdentityNamespaceMetadataKey]; got != watcherRotatedIdentityNamespace {
					t.Fatalf("stale scan restored previous namespace: %v", got)
				}
			}
		})
	}
}

func TestCodexIdentityConflictRemovalKeepsRemainingDuplicatesBlocked(t *testing.T) {
	w := newCodexIdentityLifecycleWatcher(t)
	for _, id := range []string{"a.json", "b.json", "c.json"} {
		writeCodexIdentityLifecycleFile(t, w, id, watcherSharedIdentityNamespace, "token-"+id)
	}
	path := filepath.Join(w.authDir, "c.json")
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatal(errRemove)
	}
	w.removeClient(path)
	requireCodexIdentityConflict(t, w, "a.json", true)
	requireCodexIdentityConflict(t, w, "b.json", true)
}

func TestCodexIdentityConflictChangesDoNotDiscardOtherScannedFileContents(t *testing.T) {
	for _, action := range []string{"add", "remove", "rotate"} {
		t.Run(action, func(t *testing.T) {
			w := newCodexIdentityLifecycleWatcher(t)
			writeCodexIdentityLifecycleFile(t, w, "a.json", watcherSharedIdentityNamespace, "old-token")
			if action != "add" {
				writeCodexIdentityLifecycleFile(t, w, "b.json", watcherSharedIdentityNamespace, "token-b")
			}
			metadata := w.currentAuths["a.json"].Clone().Metadata
			metadata["access_token"] = "scanned-token"
			raw, errMarshal := json.Marshal(metadata)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if errWrite := os.WriteFile(filepath.Join(w.authDir, "a.json"), raw, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			finishScan := pauseAuthSnapshot(t, w, false)
			switch action {
			case "add":
				writeCodexIdentityLifecycleFile(t, w, "b.json", watcherSharedIdentityNamespace, "token-b")
			case "rotate":
				writeCodexIdentityLifecycleFile(t, w, "b.json", watcherRotatedIdentityNamespace, "token-b")
			case "remove":
				path := filepath.Join(w.authDir, "b.json")
				if errRemove := os.Remove(path); errRemove != nil {
					t.Fatal(errRemove)
				}
				w.removeClient(path)
			}
			finishScan()
			requireCodexIdentityConflict(t, w, "a.json", action == "add")
			for source, auth := range map[string]*coreauth.Auth{"current": w.currentAuths["a.json"], "queued": w.pendingUpdates["a.json"].Auth} {
				if got := auth.Metadata["access_token"]; got != "scanned-token" {
					t.Fatalf("%s conflict update discarded scanned token: %v", source, got)
				}
			}
		})
	}
}

func TestCodexIdentityConflictsReconcileRuntimeAndFileOwners(t *testing.T) {
	w := newCodexIdentityLifecycleWatcher(t)
	writeCodexIdentityLifecycleFile(t, w, "a.json", watcherSharedIdentityNamespace, "file-token")
	runtimeAuth := w.currentAuths["a.json"].Clone()
	runtimeAuth.Metadata["access_token"] = "runtime-token"
	if !w.DispatchRuntimeAuthUpdate(AuthUpdate{Action: AuthUpdateActionModify, Auth: runtimeAuth}) {
		t.Fatal("runtime refresh was not dispatched")
	}
	w.refreshAuthState(true)
	requireCodexIdentityConflict(t, w, "a.json", false)
	if got := w.currentAuths["a.json"].Metadata["access_token"]; got != "runtime-token" {
		t.Fatalf("runtime override was replaced by file snapshot: %v", got)
	}

	other := codexWatcherIdentityAuth("runtime-b", watcherSharedIdentityNamespace)
	if !w.DispatchRuntimeAuthUpdate(AuthUpdate{Action: AuthUpdateActionAdd, Auth: other}) {
		t.Fatal("runtime duplicate was not dispatched")
	}
	requireCodexIdentityConflict(t, w, "a.json", true)
	requireCodexIdentityConflict(t, w, "runtime-b", true)
	other.Metadata["access_token"] = "refreshed-runtime-token"
	w.DispatchRuntimeAuthUpdate(AuthUpdate{Action: AuthUpdateActionModify, Auth: other})
	requireCodexIdentityConflict(t, w, "runtime-b", true)
	w.refreshAuthState(true)
	requireCodexIdentityConflict(t, w, "a.json", true)
	requireCodexIdentityConflict(t, w, "runtime-b", true)
	w.DispatchRuntimeAuthUpdate(AuthUpdate{Action: AuthUpdateActionDelete, ID: "runtime-b"})
	requireCodexIdentityConflict(t, w, "a.json", false)
	w.refreshAuthState(true)
	requireCodexIdentityConflict(t, w, "a.json", false)
}

func TestCodexIdentitySnapshotDeduplicatesVirtualAuthIDsBeforeConflicts(t *testing.T) {
	w := newCodexIdentityLifecycleWatcher(t)
	first := codexWatcherIdentityAuth("virtual-a", watcherSharedIdentityNamespace)
	coreauth.MarkPluginVirtualAuth(first, filepath.Join(w.authDir, "plugin.json"), 0)
	replacement := first.Clone()
	replacement.Metadata["access_token"] = "latest-token"
	w.dispatchAuthUpdates(w.prepareAuthUpdatesLocked([]*coreauth.Auth{first, replacement}, false))
	requireCodexIdentityConflict(t, w, "virtual-a", false)
	if got := w.currentAuths["virtual-a"].Metadata["access_token"]; got != "latest-token" {
		t.Fatalf("duplicate ID did not preserve last entry: %v", got)
	}

	// Different virtual credential IDs must still remain separate owners.
	second := codexWatcherIdentityAuth("virtual-b", watcherSharedIdentityNamespace)
	coreauth.MarkPluginVirtualAuth(second, filepath.Join(w.authDir, "plugin.json"), 1)
	w.dispatchAuthUpdates(w.prepareAuthUpdatesLocked([]*coreauth.Auth{first, replacement, second}, false))
	requireCodexIdentityConflict(t, w, "virtual-a", true)
	requireCodexIdentityConflict(t, w, "virtual-b", true)
}

func newCodexIdentityLifecycleWatcher(t *testing.T) *Watcher {
	t.Helper()
	dir := t.TempDir()
	return &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 16)}
}

func writeCodexIdentityLifecycleFile(t *testing.T, w *Watcher, id, namespace, token string) {
	t.Helper()
	raw, errMarshal := json.Marshal(map[string]any{
		"type": "codex", "access_token": token,
		codex.CredentialIdentityVersionMetadataKey:   1,
		codex.CredentialIdentityNamespaceMetadataKey: namespace,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	path := filepath.Join(w.authDir, id)
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if !w.addOrUpdateClient(path) {
		t.Fatalf("could not load credential %s", id)
	}
}

func requireCodexIdentityConflict(t *testing.T, w *Watcher, id string, conflict bool) {
	t.Helper()
	for source, auth := range map[string]*coreauth.Auth{"current": w.currentAuths[id], "queued": w.pendingUpdates[id].Auth} {
		if auth == nil {
			t.Fatalf("%s credential %s is missing", source, id)
		}
		if got := auth.Attributes[codex.CredentialIdentityConflictAttribute] == "true"; got != conflict {
			t.Fatalf("%s credential %s conflict = %t, want %t", source, id, got, conflict)
		}
	}
	if w.pendingUpdates[id].revision != w.authRevisions[id] {
		t.Fatalf("queued credential %s revision is stale", id)
	}
}
