package pluginhost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestHostHTTPExecutionProxyPreservesCredentialRoute(t *testing.T) {
	for _, wire := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, scenario := range []string{"conflict", "credential-direct", "invalid", "matching", "unbound"} {
				t.Run(scenario+"/wire="+stringBool(wire)+"/stream="+stringBool(stream), func(t *testing.T) {
					var proxyCalls, otherCalls, originCalls atomic.Int32
					proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						proxyCalls.Add(1)
						_, _ = io.WriteString(w, "chosen-proxy")
					}))
					defer proxy.Close()
					other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						otherCalls.Add(1)
						_, _ = io.WriteString(w, "wrong-proxy")
					}))
					defer other.Close()
					origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						originCalls.Add(1)
						_, _ = io.WriteString(w, "wrong-direct")
					}))
					defer origin.Close()
					auth := &coreauth.Auth{ID: "credential", ProxyURL: proxy.URL}
					override := other.URL
					wantError := true
					switch scenario {
					case "credential-direct":
						auth.ProxyURL = "direct"
					case "invalid":
						override = "http://secret-user:secret-pass@localhost:99999"
					case "matching":
						override, wantError = proxy.URL, false
					case "unbound":
						auth.ProxyURL, override, wantError = "", proxy.URL, false
					}
					host := New()
					host.runtimeConfig = &config.Config{SDKConfig: config.SDKConfig{ProxyURL: other.URL}}
					client := host.newHTTPClientWithProxy(auth, override)
					ctx := coreexecutor.WithUpstreamAttemptTracker(coreexecutor.WithRequestProxyURL(context.Background(), other.URL))
					req := pluginapi.HTTPRequest{URL: origin.URL}
					if wire {
						req.WireProfile = &pluginapi.HTTPWireProfile{HTTP1Only: true, HeaderProfile: []string{"Host"}}
					}
					var errRequest error
					var body []byte
					if stream {
						var response pluginapi.HTTPStreamResponse
						response, errRequest = client.DoStream(ctx, req)
						if errRequest == nil {
							for chunk := range response.Chunks {
								body = append(body, chunk.Payload...)
								if chunk.Err != nil {
									errRequest = chunk.Err
								}
							}
						}
					} else {
						var response pluginapi.HTTPResponse
						response, errRequest = client.Do(ctx, req)
						body = response.Body
					}
					if wantError {
						var routeErr *proxyutil.ExecutionProxyError
						if !errors.As(errRequest, &routeErr) || routeErr.StatusCode() != 400 || !routeErr.IsRequestScoped() {
							t.Fatalf("request error = %v, want typed request-scoped 400", errRequest)
						}
						if coreexecutor.UpstreamAttempted(ctx) || proxyCalls.Load() != 0 || otherCalls.Load() != 0 || originCalls.Load() != 0 {
							t.Fatal("invalid/conflicting override attempted network")
						}
						if strings.Contains(errRequest.Error(), "secret") || strings.Contains(errRequest.Error(), override) {
							t.Fatal("route error leaked proxy URL")
						}
					} else if errRequest != nil || string(body) != "chosen-proxy" || proxyCalls.Load() != 1 || otherCalls.Load() != 0 || originCalls.Load() != 0 || !coreexecutor.UpstreamAttempted(ctx) {
						t.Fatalf("route/body/calls = %v/%q/%d/%d/%d", errRequest, body, proxyCalls.Load(), otherCalls.Load(), originCalls.Load())
					}
				})
			}
		}
	}
}

func stringBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
