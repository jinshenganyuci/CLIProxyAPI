package executor

import (
	"context"
	"fmt"
	"strings"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func validateCodexCredentialProxyPolicy(cfg *config.Config, auth *cliproxyauth.Auth, contexts ...context.Context) error {
	if len(contexts) > 0 && cliproxyexecutor.RequestProxyURL(contexts[0]) != "" {
		required := cfg != nil && strings.EqualFold(cfg.Codex.CredentialProxyPolicy, config.CodexCredentialProxyPolicyRequire)
		oauth := auth == nil || !codexAuthUsesAPIKey(auth)
		if !(required && oauth && (auth == nil || strings.TrimSpace(auth.ProxyURL) == "")) {
			_, errProxy := helps.ResolveExecutionProxy(contexts[0], cfg, auth)
			return errProxy
		}
	}
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
