package auth

import (
	"strconv"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialconcurrency"
)

// ApplyAuthConcurrencyLimitMetadata keeps source-file limits on plugin-expanded credentials.
// An explicit source value overrides plugin defaults; null clears both representations.
func ApplyAuthConcurrencyLimitMetadata(auth *Auth, metadata map[string]any) error {
	if auth == nil {
		return nil
	}
	value, exists := metadata[credentialconcurrency.Field]
	if !exists {
		return ValidateAuthConcurrencyLimit(auth)
	}
	if value == nil {
		delete(auth.Attributes, credentialconcurrency.Field)
		if auth.Metadata != nil {
			auth.Metadata[credentialconcurrency.Field] = nil
		}
		return nil
	}
	limit, errParse := credentialconcurrency.ParseValue(value)
	if errParse != nil {
		return errParse
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Attributes[credentialconcurrency.Field] = strconv.Itoa(limit)
	auth.Metadata[credentialconcurrency.Field] = limit
	return nil
}
