package devin

import (
	"fmt"
	"net/http"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

// ResolveOAuthProxy freezes the current login route, including an explicit direct route.
func ResolveOAuthProxy(raw string) (string, error) {
	setting, errParse := proxyutil.Parse(raw)
	if errParse != nil || setting.Mode == proxyutil.ModeInvalid {
		return "", fmt.Errorf("invalid Devin OAuth proxy URL")
	}
	if setting.Mode == proxyutil.ModeInherit || setting.Mode == proxyutil.ModeDirect {
		return "direct", nil
	}
	return setting.Raw, nil
}

// NewOAuthHTTPClient rejects invalid proxies instead of falling back to another route.
func NewOAuthHTTPClient(raw string) (*http.Client, error) {
	proxyURL, errProxy := ResolveOAuthProxy(raw)
	if errProxy != nil {
		return nil, errProxy
	}
	transport, mode, errTransport := proxyutil.BuildHTTPTransport(proxyURL)
	if errTransport != nil || transport == nil || mode == proxyutil.ModeInvalid || mode == proxyutil.ModeInherit {
		return nil, fmt.Errorf("invalid Devin OAuth proxy transport")
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

// BindOAuthProxy persists the route used during acquisition. A login which merely
// inherited the global route must not silently replace an existing account proxy.
func BindOAuthProxy(record *coreauth.Auth, proxyURL string, explicit bool, beforeSave ...func() error) error {
	if record == nil {
		return fmt.Errorf("Devin OAuth returned no authentication record")
	}
	resolved, errProxy := ResolveOAuthProxy(proxyURL)
	if errProxy != nil {
		return errProxy
	}
	if record.Metadata == nil {
		record.Metadata = make(map[string]any)
	}
	record.ProxyURL = resolved
	record.Metadata["proxy_url"] = resolved
	storage := &oauthTokenStorage{metadata: record.Metadata, proxyURL: resolved, explicit: explicit, acquiredKeys: make(map[string]bool, len(record.Metadata))}
	for key := range record.Metadata {
		storage.acquiredKeys[key] = true
	}
	if len(beforeSave) > 0 {
		storage.beforeSave = beforeSave[0]
	}
	record.Storage = storage
	return nil
}
