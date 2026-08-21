package watcher

import (
	"testing"

	internalcodex "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestMarkCodexCredentialIdentityConflicts(t *testing.T) {
	shared := "401cdbad-c282-4534-a431-fe57eceff695"
	auths := []*coreauth.Auth{
		codexWatcherIdentityAuth("a", shared),
		codexWatcherIdentityAuth("b", shared),
		codexWatcherIdentityAuth("c", "0770adbd-c51f-46fe-a365-804bbd35ce69"),
	}
	markCodexCredentialIdentityConflicts(auths)
	for index := 0; index < 2; index++ {
		if auths[index].Attributes[internalcodex.CredentialIdentityConflictAttribute] != "true" {
			t.Fatalf("auth %s conflict marker missing", auths[index].ID)
		}
	}
	if got := auths[2].Attributes[internalcodex.CredentialIdentityConflictAttribute]; got != "" {
		t.Fatalf("unique auth conflict marker = %q", got)
	}

	auths[1].Metadata[internalcodex.CredentialIdentityNamespaceMetadataKey] = "c305f6e0-40cf-4479-af7b-0c1a359e7c4f"
	markCodexCredentialIdentityConflicts(auths)
	for _, auth := range auths {
		if got := auth.Attributes[internalcodex.CredentialIdentityConflictAttribute]; got != "" {
			t.Fatalf("stale conflict marker on %s = %q", auth.ID, got)
		}
	}
}

func codexWatcherIdentityAuth(id string, namespace string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:         id,
		Provider:   "codex",
		Attributes: map[string]string{},
		Metadata: map[string]any{
			"access_token": "secret",
			internalcodex.CredentialIdentityVersionMetadataKey:   1,
			internalcodex.CredentialIdentityNamespaceMetadataKey: namespace,
		},
	}
}
