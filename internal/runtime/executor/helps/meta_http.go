package helps

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

var metaTransportCache = NewTransportCache[string](DefaultTransportCacheCapacity)

// MetaProxyURL resolves and validates the selected credential or global route.
func MetaProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) (string, error) {
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if _, errParse := proxyutil.Parse(proxyURL); errParse != nil {
		return "", fmt.Errorf("meta proxy transport: %w", errParse)
	}
	return proxyURL, nil
}

// NewMetaHTTPClient keeps the selected route authoritative. Only an unset route
// may inherit an injected transport or the process defaults.
func NewMetaHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) (*http.Client, error) {
	proxyURL, errProxy := MetaProxyURL(cfg, auth)
	if errProxy != nil {
		return nil, errProxy
	}
	client := &http.Client{Timeout: timeout}
	if proxyURL != "" {
		transport, errTransport := metaTransportCache.Get(proxyURL, func() (*http.Transport, error) {
			transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
			return transport, errBuild
		})
		if errTransport != nil {
			return nil, fmt.Errorf("meta proxy transport: %w", errTransport)
		}
		client.Transport = transport
		return client, nil
	}
	if ctx != nil {
		if transport, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && transport != nil {
			client.Transport = transport
		}
	}
	return client, nil
}
