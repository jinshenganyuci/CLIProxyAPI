package live

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func validateLiveCredentialProxyPolicy(cfg *config.Config, selected *auth.Auth) error {
	if cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.Codex.CredentialProxyPolicy), config.CodexCredentialProxyPolicyRequire) {
		return nil
	}
	if selected != nil && selected.AuthKind() == auth.AuthKindAPIKey {
		return nil
	}
	if selected == nil {
		return fmt.Errorf("Codex credential proxy policy requires a selected OAuth credential")
	}
	setting, errParse := proxyutil.Parse(selected.ProxyURL)
	if errParse != nil || setting.Mode == proxyutil.ModeInvalid {
		return fmt.Errorf("Codex credential proxy for auth %q is invalid", selected.ID)
	}
	if setting.Mode == proxyutil.ModeInherit {
		return fmt.Errorf("Codex credential proxy for auth %q is required; configure proxy-url or explicit direct", selected.ID)
	}
	return nil
}
