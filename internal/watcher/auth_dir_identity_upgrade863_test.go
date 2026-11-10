package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV863AuthDirSwitchRescansCodexIdentityWithoutOldOwners(t *testing.T) {
	w := newCodexIdentityLifecycleWatcher(t)
	dirA := w.authDir
	writeCodexIdentityLifecycleFile(t, w, "shared.json", watcherSharedIdentityNamespace, "old-shared")
	writeCodexIdentityLifecycleFile(t, w, "old-owner.json", watcherSharedIdentityNamespace, "old-owner")
	requireCodexIdentityConflict(t, w, "shared.json", true)
	dirB := t.TempDir()
	writeV863IdentityFile(t, dirB, "shared.json", watcherRotatedIdentityNamespace, "new-shared")
	writeV863IdentityFile(t, dirB, "new-owner.json", watcherSharedIdentityNamespace, "new-owner")

	originalSnapshot := snapshotCoreAuthsFunc
	t.Cleanup(func() { snapshotCoreAuthsFunc = originalSnapshot })
	scans := 0
	snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
		auths, incomplete := originalSnapshot(cfg, authDir, parser)
		scans++
		if scans == 1 {
			w.SetConfig(&config.Config{AuthDir: dirB})
		}
		return auths, incomplete
	}
	w.refreshAuthState(false)
	if scans != 2 || w.activeAuthScans != 0 {
		t.Fatalf("directory switch did not complete one replacement scan: scans=%d active=%d", scans, w.activeAuthScans)
	}
	if len(w.currentAuths) != 2 || w.currentAuths["old-owner.json"] != nil {
		t.Fatal("retired directory still owns a credential")
	}
	for id, token := range map[string]string{"shared.json": "new-shared", "new-owner.json": "new-owner"} {
		requireCodexIdentityConflict(t, w, id, false)
		for _, auth := range []*coreauth.Auth{w.currentAuths[id], w.pendingUpdates[id].Auth} {
			if auth == nil || auth.Metadata["access_token"] != token || !w.sameNormalizedAuthPath(auth.FileSnapshotPath(), filepath.Join(dirB, id)) {
				t.Fatalf("credential %s did not retain its new directory snapshot", id)
			}
		}
	}
	if w.currentAuths["shared.json"].Metadata[codex.CredentialIdentityNamespaceMetadataKey] != watcherRotatedIdentityNamespace {
		t.Fatal("same-name credential retained the retired namespace")
	}
	for path := range w.fileAuthsByPath {
		if w.isPathInAuthDir(path, dirA) {
			t.Fatal("retired directory remains indexed after replacement scan")
		}
	}
	if update := w.pendingUpdates["old-owner.json"]; update.Action != AuthUpdateActionDelete || update.Revision() != w.authRevisions[update.ID] {
		t.Fatal("retired owner deletion was not revision stamped")
	}
}

func TestV863PersistedAuthDispatchRejectsRetiredAuthDirectory(t *testing.T) {
	for _, phase := range []string{"before-read", "during-synthesis"} {
		t.Run(phase, func(t *testing.T) {
			w := newCodexIdentityLifecycleWatcher(t)
			id := "shared.json"
			writeCodexIdentityLifecycleFile(t, w, id, watcherSharedIdentityNamespace, "accepted-token")
			path := filepath.Join(w.authDir, id)
			accepted := w.currentAuths[id].Clone()
			acceptedHash := w.lastAuthHashes[w.normalizeAuthPath(path)]
			revision := w.authRevisions[id]
			sourceRevision := w.authSourceRevisions[id]
			writeV863IdentityFile(t, w.authDir, id, watcherSharedIdentityNamespace, "retired-token")
			dirB := t.TempDir()
			switchDirectory := func() { w.SetConfig(&config.Config{AuthDir: dirB}) }
			if phase == "before-read" {
				switchDirectory()
			} else {
				w.SetPluginAuthParser(persistedAuthParserFunc(func(_ context.Context, _ pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
					switchDirectory()
					return nil, false, nil
				}))
			}
			update := AuthUpdate{Action: AuthUpdateActionModify, ID: id, Auth: accepted}
			if ok, revision := w.DispatchPersistedAuthUpdateWithRevision(&update); ok || revision != 0 {
				t.Fatalf("retired persisted snapshot was accepted: ok=%t revision=%d", ok, revision)
			}
			if w.authRevisions[id] != revision || w.authSourceRevisions[id] != sourceRevision || w.persistedAuthRevisions[id] != 0 {
				t.Fatal("rejected persisted snapshot changed source or delivery revisions")
			}
			for _, auth := range []*coreauth.Auth{w.currentAuths[id], w.pendingUpdates[id].Auth} {
				if auth == nil || auth.Metadata["access_token"] != "accepted-token" || !auth.SameFileSnapshot(accepted) {
					t.Fatal("rejected persisted snapshot overwrote the accepted credential")
				}
			}
			if w.lastAuthHashes[w.normalizeAuthPath(path)] != acceptedHash {
				t.Fatal("rejected persisted snapshot poisoned the file hash cache")
			}
		})
	}
}

func writeV863IdentityFile(t *testing.T, dir, id, namespace, token string) {
	t.Helper()
	raw := `{"type":"codex","access_token":"` + token + `","codex_identity_version":1,"codex_identity_namespace":"` + namespace + `"}`
	if errWrite := os.WriteFile(filepath.Join(dir, id), []byte(raw), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
}
