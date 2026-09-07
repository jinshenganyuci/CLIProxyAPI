package codex

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestResolveCredentialProxySetting(t *testing.T) {
	for _, test := range []struct {
		name, policy, credential, global string
		wantMode                         proxyutil.Mode
		wantError                        string
	}{
		{name: "prefer_inherit", wantMode: proxyutil.ModeInherit},
		{name: "prefer_global", global: "http://127.0.0.1:8080", wantMode: proxyutil.ModeProxy},
		{name: "prefer_invalid_global", global: "invalid", wantError: "global proxy is invalid"},
		{name: "prefer_invalid_credential", credential: "invalid", global: "direct", wantError: "selected proxy is invalid"},
		{name: "prefer_direct", credential: "direct", global: "invalid", wantMode: proxyutil.ModeDirect},
		{name: "prefer_none", credential: " NONE ", global: "invalid", wantMode: proxyutil.ModeDirect},
		{name: "prefer_http", credential: "http://127.0.0.1:8080", global: "invalid", wantMode: proxyutil.ModeProxy},
		{name: "prefer_https", credential: "https://127.0.0.1:8080", wantMode: proxyutil.ModeProxy},
		{name: "prefer_socks5", credential: "socks5://127.0.0.1:1080", wantMode: proxyutil.ModeProxy},
		{name: "prefer_socks5h", credential: "socks5h://127.0.0.1:1080", wantMode: proxyutil.ModeProxy},
		{name: "prefer_unsupported_scheme", credential: "ftp://test-user:test-password@proxy.invalid", wantError: "selected proxy is invalid"},
		{name: "require_missing", policy: "require", global: "http://127.0.0.1:8080", wantError: "credential proxy is required"},
		{name: "require_invalid", policy: "require", credential: "invalid", wantError: "selected proxy is invalid"},
		{name: "require_direct", policy: "require", credential: "direct", wantMode: proxyutil.ModeDirect},
		{name: "require_proxy", policy: "require", credential: "http://127.0.0.1:8080", wantMode: proxyutil.ModeProxy},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: test.policy}}
			cfg.ProxyURL = test.global
			setting, errProxy := ResolveCredentialProxySetting(cfg, test.credential)
			if test.wantError != "" {
				if errProxy == nil || !strings.Contains(errProxy.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", errProxy, test.wantError)
				}
				if strings.Contains(errProxy.Error(), "test-password") || strings.Contains(errProxy.Error(), "test-user") {
					t.Fatal("proxy validation exposed credentials")
				}
			} else if errProxy != nil || setting.Mode != test.wantMode {
				t.Fatalf("mode = %v, error = %v, want %v", setting.Mode, errProxy, test.wantMode)
			}
		})
	}
	if _, errProxy := ResolveCredentialProxySetting(nil, "invalid"); errProxy == nil {
		t.Fatal("nil config allowed an invalid explicit proxy")
	}
}

func TestCodexOAuthProxyNeverFallsBackFromExplicitSetting(t *testing.T) {
	for _, test := range []struct {
		name, credential, global, wantRoute string
		wantError                           bool
	}{
		{name: "invalid_credential", credential: "invalid", global: "global", wantError: true},
		{name: "invalid_global", global: "invalid", wantError: true},
		{name: "unreachable_credential", credential: "unreachable", global: "global", wantError: true},
		{name: "unreachable_global", global: "unreachable", wantError: true},
		{name: "credential_wins", credential: "credential", global: "global", wantRoute: "credential"},
		{name: "global_used", global: "global", wantRoute: "global"},
		{name: "direct_wins", credential: "direct", global: "global", wantRoute: "direct"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var directHits, credentialHits, globalHits atomic.Int32
			handler := func(hits *atomic.Int32) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(http.StatusOK) }
			}
			direct := httptest.NewServer(handler(&directHits))
			defer direct.Close()
			credential := httptest.NewServer(handler(&credentialHits))
			defer credential.Close()
			global := httptest.NewServer(handler(&globalHits))
			defer global.Close()
			unreachable := httptest.NewServer(handler(&credentialHits))
			unreachable.Close()
			resolve := func(value string) string {
				switch value {
				case "credential":
					return credential.URL
				case "global":
					return global.URL
				case "unreachable":
					return unreachable.URL
				default:
					return value
				}
			}
			cfg := &config.Config{}
			cfg.ProxyURL = resolve(test.global)
			service := NewCodexAuthWithProxyURL(cfg, resolve(test.credential))
			defer service.httpClient.CloseIdleConnections()
			response, errRequest := service.httpClient.Get(direct.URL + "/oauth/token")
			if response != nil {
				if errClose := response.Body.Close(); errClose != nil {
					t.Fatal(errClose)
				}
			}
			if (errRequest != nil) != test.wantError {
				t.Fatalf("request error = %v, want error %t", errRequest, test.wantError)
			}
			for route, hits := range map[string]*atomic.Int32{"direct": &directHits, "credential": &credentialHits, "global": &globalHits} {
				var want int32
				if route == test.wantRoute {
					want = 1
				}
				if hits.Load() != want {
					t.Errorf("%s requests = %d, want %d", route, hits.Load(), want)
				}
			}
		})
	}
}

func TestCodexOAuthExplicitDirectDisablesEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	service := NewCodexAuthWithProxyURL(nil, "direct")
	transport, ok := service.httpClient.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("explicit direct did not disable environment proxy lookup")
	}
}
