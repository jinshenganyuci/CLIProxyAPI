package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// ResolveProxySetting validates the selected route without falling back when an
// explicit setting is invalid. Empty settings retain the caller's default route.
func ResolveProxySetting(selectedURL, fallbackURL string) (proxyutil.Setting, error) {
	raw := strings.TrimSpace(selectedURL)
	source := "selected"
	if raw == "" {
		raw = strings.TrimSpace(fallbackURL)
		source = "global"
	}
	setting, errParse := proxyutil.Parse(raw)
	if errParse != nil || setting.Mode == proxyutil.ModeInvalid {
		return setting, fmt.Errorf("codex %s proxy is invalid", source)
	}
	return setting, nil
}

// ResolveCredentialProxySetting enforces the OAuth credential policy and returns
// the sole allowed route. Prefer permits inheritance only for an empty setting.
func ResolveCredentialProxySetting(cfg *config.Config, credentialURL string) (proxyutil.Setting, error) {
	globalURL := ""
	if cfg != nil {
		if strings.EqualFold(strings.TrimSpace(cfg.Codex.CredentialProxyPolicy), config.CodexCredentialProxyPolicyRequire) && strings.TrimSpace(credentialURL) == "" {
			return proxyutil.Setting{}, fmt.Errorf("codex credential proxy is required; configure proxy-url or explicit direct")
		}
		globalURL = cfg.ProxyURL
	}
	return ResolveProxySetting(credentialURL, globalURL)
}

type proxyConfigurationErrorTransport struct {
	err error
}

func (t proxyConfigurationErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}

func codexProxyRefreshRoute(setting proxyutil.Setting) string {
	switch setting.Mode {
	case proxyutil.ModeDirect:
		return "direct"
	case proxyutil.ModeProxy:
		return setting.URL.String()
	case proxyutil.ModeInherit:
		return "inherit"
	default:
		return "invalid:" + setting.Raw
	}
}

func (o *CodexAuth) refreshSingleflightKey(refreshToken string) string {
	// A token shared across credential files must not merge refreshes that have
	// different selected egress. Hash the key so it contains no proxy credentials.
	digest := sha256.Sum256([]byte(o.refreshRoute + "\x00" + refreshToken))
	return hex.EncodeToString(digest[:])
}
