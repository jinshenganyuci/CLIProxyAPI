package helps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestCredentialProxyClientsRejectInvalidRouteBeforeAnyTransport(t *testing.T) {
	var directHits, contextHits, globalHits atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer direct.Close()
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		globalHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer global.Close()
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
		contextHits.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	clients := map[string]func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client{
		"standard":      NewProxyAwareHTTPClient,
		"fingerprinted": NewUtlsHTTPClient,
	}
	for name, constructor := range clients {
		for _, source := range []string{"credential", "global"} {
			for _, raw := range []string{"bad-proxy", "ftp://user:secret@example.invalid", "http://user:secret@%zz"} {
				t.Run(name+"/"+source+"/"+raw, func(t *testing.T) {
					cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: global.URL}}
					auth := &cliproxyauth.Auth{Provider: "gemini", ProxyURL: raw}
					if source == "global" {
						auth.ProxyURL = ""
						cfg.ProxyURL = raw
					}
					for _, target := range []string{direct.URL, "https://chatgpt.com/backend-api/codex/responses", "https://api.anthropic.com/v1/messages"} {
						client := constructor(ctx, cfg, auth, 0)
						request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
						if errRequest != nil {
							t.Fatal(errRequest)
						}
						response, errDo := client.Do(request)
						if response != nil {
							_ = response.Body.Close()
						}
						if errDo == nil {
							t.Fatal("invalid selected proxy unexpectedly sent the request")
						}
						if strings.Contains(errDo.Error(), "secret") {
							t.Fatal("proxy error disclosed credentials")
						}
					}
				})
			}
		}
	}
	if directHits.Load() != 0 || contextHits.Load() != 0 || globalHits.Load() != 0 {
		t.Fatalf("unexpected fallback: direct=%d context=%d global=%d", directHits.Load(), contextHits.Load(), globalHits.Load())
	}
}

func TestCredentialProxyClientsKeepSelectedDirectAndInheritedRoutes(t *testing.T) {
	var directHits, selectedHits, globalHits, contextHits atomic.Int32
	response := func(counter *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			counter.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	}
	direct := httptest.NewServer(response(&directHits))
	defer direct.Close()
	selected := httptest.NewServer(response(&selectedHits))
	defer selected.Close()
	global := httptest.NewServer(response(&globalHits))
	defer global.Close()
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
		contextHits.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	for name, constructor := range map[string]func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client{
		"standard":      NewProxyAwareHTTPClient,
		"fingerprinted": NewUtlsHTTPClient,
	} {
		t.Run(name, func(t *testing.T) {
			for _, test := range []struct {
				name, credential, global string
				counter                  *atomic.Int32
			}{
				{"selected", selected.URL, global.URL, &selectedHits},
				{"direct", "direct", global.URL, &directHits},
				{"inherited_global", "", global.URL, &globalHits},
				{"inherited_context", "", "", &contextHits},
			} {
				t.Run(test.name, func(t *testing.T) {
					before := test.counter.Load()
					client := constructor(ctx, &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: test.global}}, &cliproxyauth.Auth{ProxyURL: test.credential}, 0)
					request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, direct.URL, nil)
					if errRequest != nil {
						t.Fatal(errRequest)
					}
					response, errDo := client.Do(request)
					if errDo != nil {
						t.Fatal(errDo)
					}
					if errClose := response.Body.Close(); errClose != nil {
						t.Fatal(errClose)
					}
					client.CloseIdleConnections()
					if got := test.counter.Load(); got != before+1 {
						t.Fatalf("selected route hits=%d, want %d", got, before+1)
					}
				})
			}
		})
	}
	if directHits.Load() != 2 || selectedHits.Load() != 2 || globalHits.Load() != 2 || contextHits.Load() != 2 {
		t.Fatalf("routes crossed: direct=%d selected=%d global=%d context=%d", directHits.Load(), selectedHits.Load(), globalHits.Load(), contextHits.Load())
	}
}

func TestFingerprintConstructorsRejectInvalidProxy(t *testing.T) {
	for name, transport := range map[string]http.RoundTripper{
		"chrome":   newUtlsRoundTripper("http://user:secret@%zz"),
		"claude":   newClaudeCodeRoundTripper("http://user:secret@%zz"),
		"standard": NewProxyAwareHTTPClient(nil, nil, &cliproxyauth.Auth{ProxyURL: "bad-proxy"}, 0).Transport,
	} {
		t.Run(name, func(t *testing.T) {
			request, errRequest := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://127.0.0.1:1/", nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			body := &proxyRejectedRequestBody{Reader: strings.NewReader("synthetic request")}
			request.Body = body
			response, errRoundTrip := transport.RoundTrip(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if errRoundTrip == nil || !strings.Contains(errRoundTrip.Error(), "selected proxy") || strings.Contains(errRoundTrip.Error(), "secret") {
				t.Fatalf("unexpected proxy error: %v", errRoundTrip)
			}
			if !body.closed {
				t.Fatal("rejected request body was not closed")
			}
		})
	}
}

type proxyRejectedRequestBody struct {
	io.Reader
	closed bool
}

func (body *proxyRejectedRequestBody) Close() error {
	body.closed = true
	return nil
}

func TestCredentialProxyClientsUnreachableProxyNeverFallsBack(t *testing.T) {
	var fallbackHits atomic.Int32
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
		fallbackHits.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("wrong route"))}, nil
	}))
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	for index, constructor := range []func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client{NewProxyAwareHTTPClient, NewUtlsHTTPClient} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			client := constructor(ctx, nil, &cliproxyauth.Auth{ProxyURL: deadURL}, 0)
			request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid/", nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			response, errDo := client.Do(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if errDo == nil {
				t.Fatal("unreachable selected proxy unexpectedly succeeded")
			}
		})
	}
	if fallbackHits.Load() != 0 {
		t.Fatalf("fallback hits=%d, want 0", fallbackHits.Load())
	}
}
