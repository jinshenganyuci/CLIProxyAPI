package live

import (
	"fmt"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func validateLiveCredentialProxyPolicy(cfg *config.Config, selected *auth.Auth) error {
	if selected != nil && selected.AuthKind() == auth.AuthKindAPIKey {
		return nil
	}
	proxyURL := ""
	if selected != nil {
		proxyURL = selected.ProxyURL
	}
	_, errProxy := codexauth.ResolveCredentialProxySetting(cfg, proxyURL)
	if errProxy == nil {
		return nil
	}
	if selected == nil {
		return errProxy
	}
	return fmt.Errorf("%w for auth %q", errProxy, selected.ID)
}
