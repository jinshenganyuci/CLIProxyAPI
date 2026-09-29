package codex

import "testing"

func TestPlanRefreshPartialStoragePreservesCredentialFieldsUpgrade804(t *testing.T) {
	storage := &CodexTokenStorage{
		IDToken: "old-id", AccessToken: "old-access", RefreshToken: "old-refresh", AccountID: "old-account", Email: "old@example.test",
		IdentityVersion: CredentialIdentityCurrentVersion, IdentityNamespace: "ac411233-8de5-44c6-ac02-7ccefb2a16dc", PlanType: "pro",
	}
	service := &CodexAuth{}
	service.UpdateTokenStorage(storage, &CodexTokenData{AccessToken: "new-access", Expire: "2099-01-01T00:00:00Z", PlanType: " team "})
	if storage.RefreshToken != "old-refresh" || storage.AccountID != "old-account" || storage.IDToken != "old-id" || storage.Email != "old@example.test" {
		t.Fatal("partial plan refresh erased an omitted credential field")
	}
	if storage.AccessToken != "new-access" || storage.PlanType != "team" || storage.IdentityNamespace != "ac411233-8de5-44c6-ac02-7ccefb2a16dc" || storage.IdentityVersion != CredentialIdentityCurrentVersion {
		t.Fatal("partial plan refresh lost access token, plan or credential identity")
	}
	service.UpdateTokenStorage(storage, &CodexTokenData{AccessToken: "next-access", RefreshToken: "rotated-refresh", AccountID: "updated-account", IDToken: "updated-id", Email: "updated@example.test"})
	if storage.RefreshToken != "rotated-refresh" || storage.AccountID != "updated-account" || storage.IDToken != "updated-id" || storage.Email != "updated@example.test" || storage.PlanType != DefaultPlanType {
		t.Fatal("present rotated fields or native free fallback were not applied")
	}
}
