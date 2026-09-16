package meta

import (
	"encoding/json"
	"fmt"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// BindOAuthProxy persists the route used during acquisition. A login which merely
// inherited the global route must not silently replace an existing account proxy.
func BindOAuthProxy(record *coreauth.Auth, proxyURL string, explicit bool, beforeSave ...func() error) error {
	if record == nil {
		return fmt.Errorf("Meta OAuth returned no authentication record")
	}
	resolved, errProxy := ResolveOAuthProxy(proxyURL)
	if errProxy != nil {
		return errProxy
	}
	if record.Metadata == nil {
		record.Metadata = make(map[string]any)
	}
	token, ok := record.Storage.(*MetaTokenStorage)
	if !ok || token == nil {
		// Metadata-only callers still use the native field and omission rules.
		raw, errMarshal := json.Marshal(record.Metadata)
		if errMarshal != nil {
			return fmt.Errorf("encode Meta OAuth acquisition metadata: %w", errMarshal)
		}
		token = &MetaTokenStorage{}
		if errDecode := json.Unmarshal(raw, token); errDecode != nil {
			return fmt.Errorf("decode Meta OAuth acquisition metadata: %w", errDecode)
		}
	}
	// Capture the acquired storage before generic metadata merging or auth hooks.
	// Native Meta storage deliberately ignores metadata for managed credentials.
	raw, errMarshal := json.Marshal(token)
	if errMarshal != nil {
		return fmt.Errorf("encode Meta OAuth credentials: %w", errMarshal)
	}
	credentials := make(map[string]any)
	if errDecode := json.Unmarshal(raw, &credentials); errDecode != nil {
		return fmt.Errorf("decode Meta OAuth credentials: %w", errDecode)
	}
	for key := range credentials {
		if _, managed := metaCredentialFields[key]; !managed {
			delete(credentials, key)
		}
	}
	credentials["type"] = "meta"
	credentials["auth_kind"] = "oauth"
	if token.ExpiresIn <= 0 {
		delete(credentials, "expires_in")
	}
	if token.DCAExpiresAt <= 0 {
		delete(credentials, "dca_expires_at")
	}
	record.ProxyURL = resolved
	record.Metadata["proxy_url"] = resolved
	storage := &oauthTokenStorage{
		metadata: record.Metadata, proxyURL: resolved, explicit: explicit,
		acquiredKeys: make(map[string]bool, len(record.Metadata)), credentials: credentials,
	}
	for key := range record.Metadata {
		storage.acquiredKeys[key] = true
	}
	if len(beforeSave) > 0 {
		storage.beforeSave = beforeSave[0]
	}
	record.Storage = storage
	return nil
}
