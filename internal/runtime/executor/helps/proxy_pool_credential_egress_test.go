package helps

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

func TestProxyPoolPreservesCredentialEgress(t *testing.T) {
	for name, constructor := range map[string]func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client{
		"standard": NewProxyAwareHTTPClient,
		"uTLS":     NewUtlsHTTPClient,
	} {
		t.Run(name, func(t *testing.T) {
			ResetProxyTransports()
			t.Cleanup(ResetProxyTransports)
			var selectedHits, selectedConnections, fallbackHits atomic.Int32
			selected := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				selectedHits.Add(1)
				_, _ = io.WriteString(w, "credential route")
			}))
			selected.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					selectedConnections.Add(1)
				}
			}
			selected.Start()
			defer selected.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer fallback.Close()
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
				fallbackHits.Add(1)
				return nil, io.ErrUnexpectedEOF
			}))
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}}
			auth := poolOptInAuth("bound-api-key")
			auth.ProxyURL = selected.URL
			matching := cliproxyexecutor.WithRequestProxyURL(ctx, selected.URL)
			for i := 0; i < 2; i++ {
				response, errDo := constructor(matching, cfg, auth, 0).Get(fallback.URL)
				if errDo != nil {
					t.Fatal(errDo)
				}
				if _, errRead := io.Copy(io.Discard, response.Body); errRead != nil {
					t.Fatal(errRead)
				}
				if errClose := response.Body.Close(); errClose != nil {
					t.Fatal(errClose)
				}
			}
			conflicting := cliproxyexecutor.WithRequestProxyURL(ctx, fallback.URL)
			for _, target := range []string{fallback.URL, "https://api.anthropic.com/v1/messages", "https://chatgpt.com/backend-api/codex/responses"} {
				_, errDo := constructor(conflicting, cfg, auth, 0).Get(target)
				var policy *proxyutil.ExecutionProxyError
				if !errors.As(errDo, &policy) || !policy.IsRequestScoped() {
					t.Fatalf("conflicting pooled request was not rejected before transport: %v", errDo)
				}
			}
			if selectedHits.Load() != 2 || selectedConnections.Load() != 1 || fallbackHits.Load() != 0 {
				t.Fatalf("hits=%d connections=%d fallback=%d, want 2/1/0", selectedHits.Load(), selectedConnections.Load(), fallbackHits.Load())
			}
		})
	}
}

func TestProxyPoolInvalidSelectedRouteFailsClosed(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer fallback.Close()
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
		fallbackHits.Add(1)
		return nil, io.ErrUnexpectedEOF
	}))
	for name, constructor := range map[string]func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client{
		"standard": NewProxyAwareHTTPClient,
		"uTLS":     NewUtlsHTTPClient,
	} {
		t.Run(name, func(t *testing.T) {
			for _, raw := range []string{"bad-proxy", "ftp://user:secret@example.invalid", "http://user:secret@%zz"} {
				auth := poolOptInAuth("invalid-route-api-key")
				auth.ProxyURL = raw
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}}
				for _, target := range []string{fallback.URL, "https://api.anthropic.com/v1/messages", "https://chatgpt.com/backend-api/codex/responses"} {
					_, errDo := constructor(ctx, cfg, auth, 0).Get(target)
					if errDo == nil || strings.Contains(errDo.Error(), "secret") {
						t.Fatalf("invalid proxy error=%v", errDo)
					}
				}
			}
		})
	}
	if fallbackHits.Load() != 0 || proxyTransportCache.Len() != 0 {
		t.Fatalf("invalid routes escaped rejection: fallback=%d pools=%d", fallbackHits.Load(), proxyTransportCache.Len())
	}
}

func TestProxyPoolExcludesCodexOAuthIncludingImportedOptIn(t *testing.T) {
	for _, test := range []struct {
		name        string
		attributes  map[string]string
		metadata    map[string]any
		connections int32
		pools       int
	}{
		{"oauth_token", nil, map[string]any{"access_token": "synthetic-oauth-token"}, 2, 0},
		{"oauth_explicit", map[string]string{cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth}, nil, 2, 0},
		{"siwc", map[string]string{cliproxyauth.AttributeAPIKey: "synthetic-api-key", "use_siwc": "true"}, nil, 2, 0},
		{"api_key", map[string]string{cliproxyauth.AttributeAPIKey: "synthetic-api-key"}, nil, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ResetProxyTransports()
			t.Cleanup(ResetProxyTransports)
			var connections atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "ok")
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			auth := poolOptInAuth("codex-pool-boundary")
			auth.Provider = "codex"
			auth.ProxyURL = "direct"
			auth.Attributes = test.attributes
			auth.Metadata = test.metadata
			for i := 0; i < 2; i++ {
				client := NewProxyAwareHTTPClient(t.Context(), nil, auth, 0)
				response, errDo := client.Get(server.URL)
				if errDo != nil {
					t.Fatal(errDo)
				}
				if _, errRead := io.Copy(io.Discard, response.Body); errRead != nil {
					t.Fatal(errRead)
				}
				if errClose := response.Body.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				if test.pools == 0 {
					client.CloseIdleConnections()
				}
			}
			if connections.Load() != test.connections || proxyTransportCache.Len() != test.pools {
				t.Fatalf("connections=%d pools=%d, want %d/%d", connections.Load(), proxyTransportCache.Len(), test.connections, test.pools)
			}
		})
	}
}
