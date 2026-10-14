package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

func TestV828DecoratorPreservesCredentialRouteAndFailClosed(t *testing.T) {
	for _, factory := range []string{"plain", "devin", "utls", "antigravity"} {
		for _, route := range []string{"selected", "direct", "conflicting-request", "invalid-selected", "selected-failure"} {
			t.Run(factory+"/"+route, func(t *testing.T) {
				var selectedHits, originHits, globalHits, injectedHits, wrappers, attempts atomic.Int32
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					originHits.Add(1)
					_, _ = io.WriteString(w, "direct")
				}))
				defer origin.Close()
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					selectedHits.Add(1)
					if route == "selected-failure" {
						w.WriteHeader(http.StatusBadGateway)
					}
					_, _ = io.WriteString(w, "selected")
				}))
				defer selected.Close()
				global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					globalHits.Add(1)
					http.Error(w, "global route forbidden", http.StatusBadGateway)
				}))
				defer global.Close()
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
					injectedHits.Add(1)
					return nil, fmt.Errorf("injected route forbidden")
				}))
				ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
					wrappers.Add(1)
					return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
						attempts.Add(1)
						return next.RoundTrip(r)
					})
				})
				auth := &coreauth.Auth{ID: t.Name(), ProxyURL: selected.URL}
				if route == "direct" {
					auth.ProxyURL = "direct"
				} else if route == "invalid-selected" {
					auth.ProxyURL = "unsupported://bad-proxy"
				} else if route == "conflicting-request" {
					ctx = coreexecutor.WithRequestProxyURL(ctx, global.URL)
				}
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL}}
				var client *http.Client
				var errClient error
				switch factory {
				case "plain":
					client = helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
				case "devin":
					client, errClient = helps.NewDevinHTTPClient(ctx, cfg, auth, 0)
				case "utls":
					client = helps.NewUtlsHTTPClient(ctx, cfg, auth, 0)
				case "antigravity":
					client = newAntigravityHTTPClient(ctx, cfg, auth, 0)
				}
				badRoute := route == "invalid-selected" || route == "conflicting-request"
				if client != nil {
					if client.Timeout != 0 {
						t.Fatal("decorator introduced an inference timeout")
					}
					request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL, nil)
					if errRequest != nil {
						t.Fatal(errRequest)
					}
					response, errDo := client.Do(request)
					errClient = errDo
					if response != nil {
						body, errRead := io.ReadAll(response.Body)
						if errClose := response.Body.Close(); errClose != nil {
							t.Fatal(errClose)
						}
						wantBody, wantStatus := "selected", http.StatusOK
						if route == "direct" {
							wantBody = "direct"
						}
						if route == "selected-failure" {
							wantStatus = http.StatusBadGateway
						}
						if errRead != nil || string(body) != wantBody || response.StatusCode != wantStatus {
							t.Fatalf("response changed: %q/%d/%v", body, response.StatusCode, errRead)
						}
					}
				}
				if (errClient != nil) != badRoute {
					t.Fatalf("route error = %v; bad route %t", errClient, badRoute)
				}
				wantWrap, wantAttempt := int32(1), int32(1)
				if factory == "devin" && badRoute {
					wantWrap, wantAttempt = 0, 0
				}
				wantSelected, wantOrigin := int32(0), int32(0)
				if !badRoute {
					if route == "direct" {
						wantOrigin = 1
					} else {
						wantSelected = 1
					}
				}
				if wrappers.Load() != wantWrap || attempts.Load() != wantAttempt || selectedHits.Load() != wantSelected || originHits.Load() != wantOrigin || globalHits.Load() != 0 || injectedHits.Load() != 0 {
					t.Fatalf("wrappers/attempts=%d/%d selected/origin/global/injected=%d/%d/%d/%d", wrappers.Load(), attempts.Load(), selectedHits.Load(), originHits.Load(), globalHits.Load(), injectedHits.Load())
				}
			})
		}
	}
}
