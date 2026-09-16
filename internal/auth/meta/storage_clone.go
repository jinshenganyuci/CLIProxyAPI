package meta

import (
	"encoding/json"

	baseauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth"
)

// MetaCredentialTokens exposes native credential identity without a provider import.
func (s *MetaTokenStorage) MetaCredentialTokens() (dcaToken, apiKey, accessToken string) {
	if s == nil {
		return "", "", ""
	}
	return s.DCAToken, s.APIKey, s.AccessToken
}

// MetaCredentialTokens identifies the credentials captured for this acquisition.
func (s *oauthTokenStorage) MetaCredentialTokens() (dcaToken, apiKey, accessToken string) {
	if s == nil {
		return "", "", ""
	}
	dcaToken, _ = s.credentials["dca_token"].(string)
	apiKey, _ = s.credentials["api_key"].(string)
	accessToken, _ = s.credentials["access_token"].(string)
	return dcaToken, apiKey, accessToken
}

// CloneTokenStorage returns independent native storage for runtime refreshes.
// Acquisition guards and frozen token snapshots must never survive into refresh.
func CloneTokenStorage(storage baseauth.TokenStorage) *MetaTokenStorage {
	switch source := storage.(type) {
	case *MetaTokenStorage:
		if source == nil {
			return nil
		}
		cloned := *source
		if source.Metadata != nil {
			raw, errEncode := json.Marshal(source.Metadata)
			if errEncode != nil {
				return nil
			}
			cloned.Metadata = nil
			if errDecode := json.Unmarshal(raw, &cloned.Metadata); errDecode != nil {
				return nil
			}
		}
		return &cloned
	case *oauthTokenStorage:
		if source == nil {
			return nil
		}
		metadata := make(map[string]any, len(source.metadata)+len(source.credentials))
		for key, value := range source.metadata {
			if _, managed := metaCredentialFields[key]; !managed {
				metadata[key] = value
			}
		}
		for key, value := range source.credentials {
			metadata[key] = value
		}
		metadata["proxy_url"] = source.proxyURL
		raw, errEncode := json.Marshal(metadata)
		if errEncode != nil {
			return nil
		}
		cloned := &MetaTokenStorage{}
		if errDecode := json.Unmarshal(raw, cloned); errDecode != nil {
			return nil
		}
		if errDecode := json.Unmarshal(raw, &cloned.Metadata); errDecode != nil {
			return nil
		}
		return cloned
	default:
		return nil
	}
}
