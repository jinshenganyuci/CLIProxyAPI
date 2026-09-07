package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveTokenToFilePreservesConcurrentlyRotatedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.json")
	oldNamespace, newNamespace := NewCredentialIdentityNamespace(), NewCredentialIdentityNamespace()
	storage := &CodexTokenStorage{
		AccessToken: "new-access", RefreshToken: "new-refresh",
		IdentityVersion: CredentialIdentityCurrentVersion, IdentityNamespace: oldNamespace,
		Metadata: map[string]any{CredentialIdentityVersionMetadataKey: 1, CredentialIdentityNamespaceMetadataKey: oldNamespace},
	}
	current := map[string]any{"type": "codex", "access_token": "old-access", CredentialIdentityVersionMetadataKey: 1, CredentialIdentityNamespaceMetadataKey: newNamespace}
	raw, errMarshal := json.Marshal(current)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errSave := storage.SaveTokenToFile(path); errSave != nil {
		t.Fatal(errSave)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errParse := json.Unmarshal(raw, &current); errParse != nil {
		t.Fatal(errParse)
	}
	if current[CredentialIdentityNamespaceMetadataKey] != newNamespace || current["access_token"] != "new-access" || current["refresh_token"] != "new-refresh" {
		t.Fatal("late token save lost the new tokens or reverted the rotated identity")
	}
}
