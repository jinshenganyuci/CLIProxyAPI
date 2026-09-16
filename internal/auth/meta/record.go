package meta

import (
	"fmt"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"strings"
)

// CreateAuthRecord preserves the native DCA and minted API key lifecycle metadata.
func CreateAuthRecord(tokenStorage *MetaTokenStorage) (*coreauth.Auth, error) {
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
		return nil, fmt.Errorf("meta token storage missing access token")
	}
	fileName := CredentialFileName(tokenStorage.Email, tokenStorage.DCAToken)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "Meta"
	}

	metadata := map[string]any{
		"type":         "meta",
		"access_token": tokenStorage.AccessToken,
		"token_type":   tokenStorage.TokenType,
		"expires_in":   tokenStorage.ExpiresIn,
		"expired":      tokenStorage.Expired,
		"last_refresh": tokenStorage.LastRefresh,
		"base_url":     tokenStorage.BaseURL,
		"auth_kind":    "oauth",
	}
	if tokenStorage.DCAExpired != "" {
		metadata["dca_expired"] = tokenStorage.DCAExpired
	}
	if tokenStorage.DCAExpiresAt > 0 {
		metadata["dca_expires_at"] = tokenStorage.DCAExpiresAt
	}
	if tokenStorage.APIKey != "" {
		metadata["api_key"] = tokenStorage.APIKey
	}
	if tokenStorage.DCAToken != "" {
		metadata["dca_token"] = tokenStorage.DCAToken
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.Name != "" {
		metadata["name"] = tokenStorage.Name
	}

	attrs := map[string]string{
		"auth_kind": "oauth",
		"base_url":  tokenStorage.BaseURL,
	}
	if tokenStorage.APIKey != "" {
		attrs["api_key"] = tokenStorage.APIKey
	}
	if tokenStorage.DCAToken != "" {
		attrs["dca_token"] = tokenStorage.DCAToken
	}
	if tokenStorage.Email != "" {
		attrs["email"] = tokenStorage.Email
	}

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   "meta",
		FileName:   fileName,
		Label:      label,
		Storage:    tokenStorage,
		Metadata:   metadata,
		Attributes: attrs,
	}, nil
}
