package executor

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func validateCodexCredentialProxyPolicy(cfg *config.Config, auth *cliproxyauth.Auth) error {
	if cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.Codex.CredentialProxyPolicy), config.CodexCredentialProxyPolicyRequire) {
		return nil
	}
	if auth != nil && codexAuthUsesAPIKey(auth) {
		return nil
	}
	if auth == nil {
		return statusErr{code: 503, msg: "codex credential proxy policy requires a selected OAuth credential"}
	}

	setting, errParse := proxyutil.Parse(auth.ProxyURL)
	if errParse != nil || setting.Mode == proxyutil.ModeInvalid {
		return statusErr{code: 503, msg: fmt.Sprintf("codex credential proxy for auth %q is invalid", auth.ID)}
	}
	if setting.Mode == proxyutil.ModeInherit {
		return statusErr{code: 503, msg: fmt.Sprintf("codex credential proxy for auth %q is required; configure proxy-url or explicit direct", auth.ID)}
	}
	return nil
}
