package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFileTokenStorePreservesConcurrentlyRotatedCodexIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential.json")
	oldNamespace, newNamespace := codex.NewCredentialIdentityNamespace(), codex.NewCredentialIdentityNamespace()
	stale := &coreauth.Auth{ID: "credential.json", Provider: "codex", Metadata: map[string]any{
		"type": "codex", "access_token": "new-access", "refresh_token": "new-refresh",
		codex.CredentialIdentityVersionMetadataKey: 1, codex.CredentialIdentityNamespaceMetadataKey: oldNamespace,
	}}
	current := map[string]any{"type": "codex", "access_token": "old-access", codex.CredentialIdentityVersionMetadataKey: 1, codex.CredentialIdentityNamespaceMetadataKey: newNamespace}
	raw, errMarshal := json.Marshal(current)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	if _, errSave := store.Save(context.Background(), stale); errSave != nil {
		t.Fatal(errSave)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errParse := json.Unmarshal(raw, &current); errParse != nil {
		t.Fatal(errParse)
	}
	if current[codex.CredentialIdentityNamespaceMetadataKey] != newNamespace || current["access_token"] != "new-access" || current["refresh_token"] != "new-refresh" {
		t.Fatal("late metadata save lost the new tokens or reverted the rotated identity")
	}
}
