package kimi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

// ResolveOAuthProxy freezes the acquisition route, including direct connections.
func ResolveOAuthProxy(raw string) (string, error) {
	setting, errParse := proxyutil.Parse(raw)
	if errParse != nil || setting.Mode == proxyutil.ModeInvalid {
		return "", fmt.Errorf("invalid Kimi OAuth proxy URL")
	}
	if setting.Mode == proxyutil.ModeInherit || setting.Mode == proxyutil.ModeDirect {
		return "direct", nil
	}
	return setting.Raw, nil
}

// NewOAuthHTTPClient never substitutes a global or direct route for an invalid proxy.
func NewOAuthHTTPClient(raw string) (*http.Client, error) {
	resolved, errProxy := ResolveOAuthProxy(raw)
	if errProxy != nil {
		return nil, errProxy
	}
	transport, mode, errTransport := proxyutil.BuildHTTPTransport(resolved)
	if errTransport != nil || transport == nil || mode == proxyutil.ModeInvalid || mode == proxyutil.ModeInherit {
		return nil, fmt.Errorf("invalid Kimi OAuth proxy transport")
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

// oauthProxyErrorTransport preserves constructor compatibility without allowing fallback.
type oauthProxyErrorTransport struct{}

func (oauthProxyErrorTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request != nil && request.Body != nil {
		_ = request.Body.Close()
	}
	return nil, fmt.Errorf("invalid Kimi OAuth proxy configuration")
}
