package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func TestV835SIWCBindingResetsOldRefreshGateAndPreservesControls(t *testing.T) {
	m, base, path, _ := setupSIWCBinding(t, "bound-controls.json")
	now := time.Now()
	disk := readBindingDisk(t, path)
	disk["codex_identity_namespace"] = "stable-bound-namespace"
	disk["concurrency_limit"] = 3
	disk["proxy_url"] = "direct"
	if err := codex.WriteJSONFileAtomic(path, disk); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	current := m.auths[base.ID]
	current.Metadata = disk
	current.ProxyURL = "direct"
	current.LastRefreshedAt = now.Add(-time.Hour)
	current.NextRefreshAfter = now.Add(5 * time.Minute)
	current.RejectedAccessToken = "old-siwc"
	current.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour)}
	current.ModelStates = map[string]*ModelState{"limited": {Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour), LastError: &Error{HTTPStatus: 503, Message: "independent failure"}}}
	snapshot := current.Clone()
	m.mu.Unlock()
	release, err := m.AcquireCredentialConcurrency(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	tokens := bindingTokens("new-bound-siwc")
	tokens.Expire = now.Add(time.Minute).Format(time.RFC3339)
	got, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, tokens)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NextRefreshAfter.IsZero() || got.LastRefreshedAt.Before(now) {
		t.Fatalf("new SIWC inherited old refresh gate: next=%v last=%v", got.NextRefreshAfter, got.LastRefreshedAt)
	}
	due, scheduled := nextRefreshCheckAt(time.Now(), got, time.Second)
	if !scheduled || due.After(time.Now()) || !m.shouldRefresh(got, time.Now()) {
		t.Fatalf("near-expiry bound SIWC not refreshable: due=%v scheduled=%t", due, scheduled)
	}
	if got.Metadata["access_token"] != "outer" || got.Metadata["refresh_token"] != "outer-refresh" || got.Metadata["codex_identity_namespace"] != "stable-bound-namespace" || got.ProxyURL != "direct" || got.RejectedAccessToken != "" {
		t.Fatal("binding changed outer identity/routing or kept rejection")
	}
	if !got.Quota.Exceeded || got.ModelStates["limited"].LastError.HTTPStatus != 503 {
		t.Fatal("binding erased unrelated cooldown")
	}
	if n, limit := m.CredentialConcurrencySnapshot(base.ID); n != 1 || limit != 3 {
		t.Fatalf("binding changed held lease: %d/%d", n, limit)
	}
	persisted := readBindingDisk(t, path)
	if persisted["siwc"].(map[string]any)["access_token"] != "new-bound-siwc" || persisted["codex_identity_namespace"] != "stable-bound-namespace" {
		t.Fatal("bound credentials or controls not persisted")
	}
	release()
	if n, _ := m.CredentialConcurrencySnapshot(base.ID); n != 0 {
		t.Fatal("pre-bind lease did not release")
	}
}

func TestV835SIWCBindingSupportsLongCredentialBasename(t *testing.T) {
	name := strings.Repeat("a", 244) + ".json"
	m, base, path, _ := setupSIWCBinding(t, name)
	if _, err := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, bindingTokens("long-name-bound")); err != nil {
		t.Fatal(err)
	}
	if readBindingDisk(t, path)["siwc"].(map[string]any)["access_token"] != "long-name-bound" {
		t.Fatal("long-name binding was not persisted")
	}
}
