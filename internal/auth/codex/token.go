// Package codex provides authentication and token management functionality
// for OpenAI's Codex AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Codex API.
package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// CodexTokenStorage stores OAuth2 token information for OpenAI Codex API authentication.
// It maintains compatibility with the existing auth system while adding Codex-specific fields
// for managing access tokens, refresh tokens, and user account information.
type CodexTokenStorage struct {
	// IDToken is the JWT ID token containing user claims and identity information.
	IDToken string `json:"id_token"`
	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens when the current one expires.
	RefreshToken string `json:"refresh_token"`
	// AccountID is the OpenAI account identifier associated with this token.
	AccountID string `json:"account_id"`
	// LastRefresh is the timestamp of the last token refresh operation.
	LastRefresh string `json:"last_refresh"`
	// Email is the OpenAI account email address associated with this token.
	Email string `json:"email"`
	// Type indicates the authentication provider type, always "codex" for this storage.
	Type string `json:"type"`
	// Expire is the timestamp when the current access token expires.
	Expire string `json:"expired"`
	// IdentityVersion is the persistent credential identity schema version.
	IdentityVersion int `json:"codex_identity_version,omitempty"`
	// IdentityNamespace is the UUID namespace owned by this OAuth credential.
	IdentityNamespace string `json:"codex_identity_namespace,omitempty"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *CodexTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the Codex token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
// It merges any injected metadata into the top-level JSON object.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *CodexTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "codex"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	// Merge metadata using helper
	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("failed to merge metadata: %w", errMerge)
	}

	var encoded bytes.Buffer
	if errEncode := json.NewEncoder(&encoded).Encode(data); errEncode != nil {
		return fmt.Errorf("failed to encode token file: %w", errEncode)
	}
	if errWrite := writeTokenFileAtomic(authFilePath, encoded.Bytes()); errWrite != nil {
		return fmt.Errorf("failed to write token file: %w", errWrite)
	}
	return nil
}

func writeTokenFileAtomic(path string, data []byte) error {
	directoryPath := filepath.Dir(path)
	temporary, errCreate := os.CreateTemp(directoryPath, ".codex-token-*.tmp")
	if errCreate != nil {
		return errCreate
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if errChmod := temporary.Chmod(0o600); errChmod != nil {
		cleanup()
		return errChmod
	}
	if _, errWrite := temporary.Write(data); errWrite != nil {
		cleanup()
		return errWrite
	}
	if errSync := temporary.Sync(); errSync != nil {
		cleanup()
		return errSync
	}
	if errClose := temporary.Close(); errClose != nil {
		_ = os.Remove(temporaryPath)
		return errClose
	}
	if errRename := os.Rename(temporaryPath, path); errRename != nil {
		_ = os.Remove(temporaryPath)
		return errRename
	}
	if errChmod := os.Chmod(path, 0o600); errChmod != nil {
		return errChmod
	}
	directory, errOpen := os.Open(directoryPath)
	if errOpen != nil {
		return errOpen
	}
	errSync := directory.Sync()
	errClose := directory.Close()
	if errSync != nil && !errors.Is(errSync, os.ErrInvalid) {
		return errSync
	}
	if errClose != nil {
		return errClose
	}
	return nil
}
