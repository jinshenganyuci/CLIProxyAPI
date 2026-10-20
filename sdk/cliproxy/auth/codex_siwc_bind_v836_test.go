package auth

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func TestV836SIWCAccountFencePreservesReplacementControlsAndLease(t *testing.T) {
	m, base, path, hook := setupSIWCBinding(t, "replaced-account.json")
	release, err := m.AcquireCredentialConcurrency(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	replacement := base.Clone()
	replacement.Metadata["account_id"] = "replacement-account"
	replacement.Metadata["access_token"] = "replacement-outer"
	replacement.Metadata["refresh_token"] = "replacement-refresh"
	replacement.Metadata["proxy_url"] = "direct"
	replacement.Metadata["codex_identity_namespace"] = "681df939-6c93-4408-9bc1-b20e8ac3be57"
	replacement.Metadata["concurrency_limit"] = 2
	replacement.ProxyURL = "direct"
	replacement.NextRefreshAfter = time.Now().Add(time.Hour)
	if errWrite := codex.WriteJSONFileAtomic(path, replacement.Metadata); errWrite != nil {
		t.Fatal(errWrite)
	}
	current, errUpdate := m.Update(context.Background(), replacement)
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if current.RegistrationEpoch != base.RegistrationEpoch {
		t.Fatal("replacement must keep epoch for this account-fence regression")
	}
	before, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	updates := hook.updates
	if _, errBind := m.BindCodexSIWC(context.Background(), base.ID, base.RegistrationEpoch, "account", bindingTokens("stale-flow")); errBind == nil {
		t.Fatal("old-account flow bound to the replacement credential")
	}
	after, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	got, _ := m.GetByID(base.ID)
	if !bytes.Equal(before, after) || hook.updates != updates || got.CredentialVersion != current.CredentialVersion || got.Generation != current.Generation || !got.NextRefreshAfter.Equal(current.NextRefreshAfter) {
		t.Fatal("rejected old-account bind mutated replacement state")
	}
	if got.ProxyURL != "direct" || got.Metadata["codex_identity_namespace"] != replacement.Metadata["codex_identity_namespace"] || got.Metadata["access_token"] != "replacement-outer" || got.Metadata["siwc"].(map[string]any)["access_token"] != "old-siwc" {
		t.Fatal("replacement credentials or controls changed")
	}
	if n, limit := m.CredentialConcurrencySnapshot(base.ID); n != 1 || limit != 2 {
		t.Fatalf("rejected bind changed held lease: %d/%d", n, limit)
	}
	release()
	if n, _ := m.CredentialConcurrencySnapshot(base.ID); n != 0 {
		t.Fatal("pre-replacement lease did not release")
	}
}
