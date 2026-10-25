package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV842PersistedCanonicalSnapshotKeepsControlsCooldownAndLease(t *testing.T) {
	ctx := coreauth.WithSkipPersist(context.Background())
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	metadata := map[string]any{"type": "codex", "access_token": "outer", "account_id": "synthetic", "proxy_url": "direct", "concurrency_limit": 3, "codex_identity_version": 1, "codex_identity_namespace": "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7", "note": "initial"}
	write := func(note string) {
		t.Helper()
		metadata["note"] = note
		data, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("initial")
	w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 4)}
	w.addOrUpdateClient(path)
	first, ok := takeQueuedAuthUpdate(w, "codex.json")
	if !ok || first.Auth == nil {
		t.Fatal("initial file not queued")
	}
	manager := coreauth.NewManager(nil, nil, nil)
	auth, err := manager.Register(ctx, first.Auth)
	if err != nil {
		t.Fatal(err)
	}
	release, err := manager.AcquireCredentialConcurrency(ctx, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	retry := time.Minute
	manager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: "codex", Model: "synthetic-model", CredentialVersion: auth.CredentialVersion, Error: &coreauth.Error{HTTPStatus: 429, Message: "synthetic quota"}, RetryAfter: &retry})
	before, _ := manager.GetByID(auth.ID)
	if !before.Quota.Exceeded || before.ModelStates["synthetic-model"] == nil {
		t.Fatal("fixture did not acquire runtime cooldown")
	}
	write("persisted-latest")
	misleading := before.Clone()
	misleading.Metadata["note"] = "unpersisted"
	misleading.ProxyURL = "http://unpersisted.invalid:8080"
	update := AuthUpdate{Action: AuthUpdateActionModify, ID: auth.ID, Auth: misleading}
	queued, revision := w.DispatchPersistedAuthUpdateWithRevision(&update)
	if !queued || revision == 0 || update.Auth == nil || !update.Auth.HasFileSnapshot() || !update.Auth.FileSnapshotMatchesDisk() || update.Auth.Metadata["note"] != "persisted-latest" || update.Auth.ProxyURL != "direct" {
		t.Fatal("persisted notification trusted memory instead of the file-fenced canonical record")
	}
	snapshot := update.Auth.Clone()
	if _, err = manager.Update(ctx, update.Auth); err != nil {
		t.Fatal(err)
	}
	current, _ := manager.GetByID(auth.ID)
	if current.RegistrationEpoch != auth.RegistrationEpoch || current.ProxyURL != "direct" || coreauth.EffectiveConcurrencyLimit(current) != 3 || current.Metadata["codex_identity_namespace"] != metadata["codex_identity_namespace"] || !current.NextRetryAfter.Equal(before.NextRetryAfter) || !current.ModelStates["synthetic-model"].NextRetryAfter.Equal(before.ModelStates["synthetic-model"].NextRetryAfter) {
		t.Fatal("file snapshot reset runtime cooldown or fork controls")
	}
	takeQueuedAuthUpdate(w, auth.ID)
	write("external-newer")
	if _, err = manager.Update(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := manager.GetByID(auth.ID)
	if unchanged.Generation != current.Generation || unchanged.Metadata["note"] != "persisted-latest" {
		t.Fatal("stale digest snapshot bypassed the manager fence")
	}
	w.RereadDiscardedFileSnapshot(snapshot)
	refreshed, ok := takeQueuedAuthUpdate(w, auth.ID)
	if !ok || refreshed.Revision() <= revision || refreshed.Auth.Metadata["note"] != "external-newer" {
		t.Fatal("discarded snapshot did not requeue the newest file revision")
	}
	if _, err = manager.Update(ctx, refreshed.Auth); err != nil {
		t.Fatal(err)
	}
	if count, limit := manager.CredentialConcurrencySnapshot(auth.ID); count != 1 || limit != 3 {
		t.Fatalf("snapshot changed held lease: %d/%d", count, limit)
	}
	release()
	if count, _ := manager.CredentialConcurrencySnapshot(auth.ID); count != 0 {
		t.Fatal("pre-snapshot lease could not release")
	}
}
