package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// ClaudeToolAliasStateScope isolates continuation aliases while allowing token
// refresh and proxy changes for the same registered credential.
func ClaudeToolAliasStateScope(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if auth == nil {
		return ""
	}
	caller, _ := opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	if caller == "" {
		caller, _ = req.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	}
	if caller == "" {
		caller = APIKeyFromContext(ctx)
	}
	if caller == "" {
		caller = opts.Headers.Get("Authorization")
	}
	identity := "id:" + strings.TrimSpace(auth.ID)
	if auth.ID == "" {
		identity = "index:" + strings.TrimSpace(auth.Index)
		if auth.Index == "" {
			if account := ClaudeCredentialAccountUUID(auth); account != "" {
				identity = "account:" + account
			} else if token := strings.TrimSpace(auth.Attributes["api_key"]); token != "" {
				identity = "key:" + token
			} else if token, _ := auth.Metadata["access_token"].(string); token != "" {
				identity = "token:" + token
			} else {
				return ""
			}
		}
	}
	baseURL := auth.Attributes["base_url"]
	if baseURL == "" {
		baseURL, _ = auth.Metadata["base_url"].(string)
	}
	data, _ := json.Marshal([]string{caller, identity, auth.Provider, strings.TrimRight(strings.TrimSpace(baseURL), "/")})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
