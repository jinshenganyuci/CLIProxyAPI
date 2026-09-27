package pluginhost

import (
	"fmt"
	"strings"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

func (h *Host) newQuotaHTTPClient(auth *coreauth.Auth, provider string) (pluginapi.HostHTTPClient, error) {
	if auth == nil {
		return h.newHTTPClient(nil, provider), nil
	}
	bound := auth.Clone()
	isCodexOAuth := strings.EqualFold(strings.TrimSpace(bound.Provider), "codex") && bound.AuthKind() != coreauth.AuthKindAPIKey
	if isCodexOAuth || strings.TrimSpace(bound.ProxyURL) != "" {
		var setting proxyutil.Setting
		var errProxy error
		if isCodexOAuth {
			setting, errProxy = codexauth.ResolveCredentialProxySetting(h.currentRuntimeConfig(), bound.ProxyURL)
		} else {
			setting, errProxy = proxyutil.Parse(bound.ProxyURL)
		}
		if errProxy != nil {
			return nil, fmt.Errorf("quota credential %q: %w", bound.ID, errProxy)
		}
		if setting.Mode == proxyutil.ModeInherit {
			bound.ProxyURL = "direct"
		} else {
			bound.ProxyURL = setting.Raw
		}
		if transport, _, errBuild := proxyutil.BuildHTTPTransport(bound.ProxyURL); errBuild != nil || transport == nil {
			return nil, fmt.Errorf("quota credential %q proxy could not be initialized", bound.ID)
		}
	}
	return h.newHTTPClient(bound, provider), nil
}

func (h *Host) quotaAuthBySelector(index, id string) (*coreauth.Auth, error) {
	if strings.TrimSpace(index) != "" {
		auth, errLookup := h.authByIndex(index)
		if errLookup != nil {
			return nil, errLookup
		}
		if id != "" && auth.ID != id {
			return nil, fmt.Errorf("quota credential selectors do not match")
		}
		return auth, nil
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return nil, fmt.Errorf("core auth manager unavailable")
	}
	if auth, ok := manager.GetByID(id); ok && auth != nil {
		return auth, nil
	}
	return nil, fmt.Errorf("quota credential not found")
}
