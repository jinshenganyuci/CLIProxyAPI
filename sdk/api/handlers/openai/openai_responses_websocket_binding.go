package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// responsesWebsocketCredentialBinding excludes mutable usage and concurrency
// settings while binding a live socket to its credential, identity and route.
func responsesWebsocketCredentialBinding(auth *coreauth.Auth, globalProxy string) string {
	if auth == nil {
		return ""
	}
	route := strings.TrimSpace(auth.ProxyURL)
	if route == "" {
		route = strings.TrimSpace(globalProxy)
	}
	if strings.EqualFold(route, "none") || strings.EqualFold(route, "direct") {
		route = "direct"
	}
	data, _ := json.Marshal([]any{
		auth.ID, auth.Provider, auth.RegistrationEpoch, route,
		auth.Attributes["api_key"], auth.Attributes["base_url"], auth.Attributes["codex_identity_conflict"],
		auth.Metadata["access_token"], auth.Metadata["account_id"],
		auth.Metadata["codex_identity_namespace"], auth.Metadata["codex_identity_version"],
	})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
