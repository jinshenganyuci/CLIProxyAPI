package meta

import (
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// ResolveOAuthProxy freezes the acquisition route, including direct connections.
func ResolveOAuthProxy(raw string) (string, error) {
	setting, errParse := proxyutil.Parse(raw)
	if errParse != nil || setting.Mode == proxyutil.ModeInvalid {
		return "", fmt.Errorf("invalid Meta OAuth proxy URL")
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
		return nil, fmt.Errorf("invalid Meta OAuth proxy transport")
	}
	return &http.Client{Transport: transport, Timeout: httpClientTimeout}, nil
}
