package management

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSaveTokenRecordPostPersistHookUsesCanonicalFileAuth(t *testing.T) {
	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir}
	h := NewHandlerWithoutConfigFilePath(cfg, nil)
	storage := codex.NewCodexAuth(cfg).CreateTokenStorage(&codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			AccountID:    "account-id",
			Email:        "diagnostic@example.test",
		},
	})
	fileName := "codex-diagnostic@example.test.json"
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "codex",
		FileName: fileName,
		Storage:  storage,
		Metadata: map[string]any{
			"email":      storage.Email,
			"account_id": storage.AccountID,
		},
	}

	var persistedHookAuth *coreauth.Auth
	h.SetPostAuthPersistHook(func(_ context.Context, auth *coreauth.Auth) error {
		persistedHookAuth = auth.Clone()
		return nil
	})
	if _, errSave := h.saveTokenRecord(context.Background(), record); errSave != nil {
		t.Fatalf("saveTokenRecord() error = %v", errSave)
	}
	if persistedHookAuth == nil {
		t.Fatal("post-persist hook auth is nil")
	}
	namespace, _, errIdentity := codex.ParseCredentialIdentity(persistedHookAuth.Metadata)
	if errIdentity != nil {
		t.Fatalf("post-persist identity invalid: %v", errIdentity)
	}
	if namespace.String() != storage.IdentityNamespace {
		t.Fatalf("post-persist namespace = %q, want %q", namespace, storage.IdentityNamespace)
	}
	if got := persistedHookAuth.Metadata["access_token"]; got != "access-token" {
		t.Fatalf("post-persist access token = %#v, want canonical persisted value", got)
	}
	if got := persistedHookAuth.Attributes[coreauth.AttributePath]; got == "" {
		t.Fatal("post-persist canonical auth path is empty")
	}
}
