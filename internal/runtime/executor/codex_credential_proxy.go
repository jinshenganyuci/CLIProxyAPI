package executor

import (
	"fmt"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func validateCodexCredentialProxyPolicy(cfg *config.Config, auth *cliproxyauth.Auth) error {
	if auth != nil && codexAuthUsesAPIKey(auth) {
		return nil
	}
	proxyURL := ""
	if auth != nil {
		proxyURL = auth.ProxyURL
	}
	_, errProxy := codexauth.ResolveCredentialProxySetting(cfg, proxyURL)
	if errProxy == nil {
		return nil
	}
	if auth == nil {
		return statusErr{code: 503, msg: errProxy.Error()}
	}
	return statusErr{code: 503, msg: fmt.Sprintf("%v for auth %q", errProxy, auth.ID)}
}
