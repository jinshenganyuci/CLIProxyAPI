package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexPlanRefreshPartialClaimsPreservesStorageAndLeaseUpgrade804(t *testing.T) {
	proxyURL, teardown := startCodexMockOAuthServers(t, "")
	defer teardown()
	const namespace = "af1c5a51-281b-46e5-9db7-2e2d27700e9e"
	storage := &codexauth.CodexTokenStorage{
		IDToken: "old-id-token", AccessToken: "old-access", RefreshToken: "old-refresh", AccountID: "old-account", Email: "old@example.test",
		PlanType: "pro", IdentityVersion: codexauth.CredentialIdentityCurrentVersion, IdentityNamespace: namespace,
	}
	directory := t.TempDir()
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(directory)
	manager := coreauth.NewManager(store, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(&config.Config{}))
	auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "partial-plan.json", FileName: "partial-plan.json", Provider: "codex", ProxyURL: proxyURL, Storage: storage,
		Metadata:   map[string]any{"type": "codex", "id_token": "old-id-token", "access_token": "old-access", "refresh_token": "old-refresh", "account_id": "old-account", "email": "old@example.test", "plan_type": "pro", "proxy_url": proxyURL, "codex_identity_version": 1, "codex_identity_namespace": namespace},
		Attributes: map[string]string{"plan_type": "pro", coreauth.AttributePath: filepath.Join(directory, "partial-plan.json")},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	lease, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), auth)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	defer lease()
	refreshed, errRefresh := manager.ForceRefreshAuth(context.Background(), auth.ID)
	if errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if refreshed.Metadata["plan_type"] != "free" || refreshed.Attributes["plan_type"] != "free" {
		t.Fatal("missing plan did not retain native free fallback")
	}
	for key, want := range map[string]string{"account_id": "old-account", "email": "old@example.test", "id_token": "old-id-token", "refresh_token": "new-mock-refresh-token", "proxy_url": proxyURL, "codex_identity_namespace": namespace} {
		if refreshed.Metadata[key] != want {
			t.Fatalf("partial refresh lost metadata field %s", key)
		}
	}
	// A persisted scoped refresh discards typed storage so later control saves
	// cannot replay a stale token snapshot over the authoritative metadata.
	if refreshed.Storage != nil {
		t.Fatal("persisted refresh retained stale typed token storage")
	}
	if storage.PlanType != "pro" || storage.AccountID != "old-account" || storage.AccessToken != "old-access" {
		t.Fatal("refresh mutated original token storage")
	}
	raw, errRead := os.ReadFile(filepath.Join(directory, auth.FileName))
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved map[string]any
	if errDecode := json.Unmarshal(raw, &saved); errDecode != nil {
		t.Fatal(errDecode)
	}
	if saved["plan_type"] != "free" || saved["account_id"] != "old-account" || saved["email"] != "old@example.test" || saved["codex_identity_namespace"] != namespace || saved["proxy_url"] != proxyURL || saved["id_token"] != "old-id-token" || saved["refresh_token"] != "new-mock-refresh-token" {
		t.Fatal("accepted refresh did not persist plan and previous controls")
	}
	if inflight, limit := manager.CredentialConcurrencySnapshot(auth.ID); inflight != 1 || limit != 10 {
		t.Fatalf("refresh detached active lease: %d/%d", inflight, limit)
	}
}
