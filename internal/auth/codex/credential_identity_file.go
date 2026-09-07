package codex

import (
	"encoding/json"
	"fmt"
	"os"
)

// PreserveFileCredentialIdentity keeps ordinary token saves from rotating an
// existing identity. The caller must hold the credential file lock through the
// subsequent write. Only explicit identity transactions replace these fields.
func PreserveFileCredentialIdentity(path string, metadata map[string]any) error {
	raw, errRead := os.ReadFile(path)
	if os.IsNotExist(errRead) {
		return nil
	}
	if errRead != nil {
		return fmt.Errorf("read existing Codex identity: %w", errRead)
	}
	var existing map[string]any
	if errParse := json.Unmarshal(raw, &existing); errParse != nil {
		return fmt.Errorf("parse existing Codex identity: %w", errParse)
	}
	if _, hasVersion := existing[CredentialIdentityVersionMetadataKey]; !hasVersion {
		if _, hasNamespace := existing[CredentialIdentityNamespaceMetadataKey]; !hasNamespace {
			return nil
		}
	}
	for _, key := range []string{CredentialIdentityVersionMetadataKey, CredentialIdentityNamespaceMetadataKey} {
		if value, exists := existing[key]; exists {
			metadata[key] = value
		} else {
			delete(metadata, key)
		}
	}
	return nil
}
