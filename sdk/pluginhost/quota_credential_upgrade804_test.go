package pluginhost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type quotaRoute804 struct{ endpoint string }

func (quotaRoute804) Identifier() string { return "quota-route-804" }
func (quotaRoute804) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{"codex"}, SupportsReset: true}, nil
}
func (p quotaRoute804) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	resp, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: p.endpoint})
	return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: string(resp.Body)}}, err
}
func (p quotaRoute804) ResetQuota(ctx context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	resp, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: p.endpoint})
	return pluginapi.QuotaResetResponse{Success: err == nil, Message: string(resp.Body)}, err
}

type quotaClientTrap804 struct{ calls atomic.Int32 }

func (c *quotaClientTrap804) Do(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.calls.Add(1)
	return pluginapi.HTTPResponse{}, fmt.Errorf("unbound client used")
}
func (c *quotaClientTrap804) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	c.calls.Add(1)
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("unbound stream client used")
}

func TestQuotaSDK804PreservesCredentialProxyPolicy(t *testing.T) {
	for _, operation := range []string{"fetch", "fetch-plugin", "reset", "reset-plugin"} {
		for _, route := range []string{"proxy", "direct", "invalid"} {
			t.Run(operation+"/"+route, func(t *testing.T) {
				var originHits, proxyHits, globalHits atomic.Int32
				newRoute := func(name string, hits *atomic.Int32) *httptest.Server {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						hits.Add(1)
						_, _ = io.WriteString(w, name)
					}))
					t.Cleanup(server.Close)
					return server
				}
				origin := newRoute("direct", &originHits)
				proxy := newRoute("proxy", &proxyHits)
				global := newRoute("global", &globalHits)
				host := New()
				host.ApplyConfig(t.Context(), RuntimeConfig{ProxyURL: global.URL})
				manager := coreauth.NewManager(nil, nil, nil)
				host.inner.SetAuthManager(manager)
				auth := &coreauth.Auth{ID: "synthetic-quota-804", Provider: "codex", ProxyURL: proxy.URL}
				if route == "direct" {
					auth.ProxyURL = "direct"
				} else if route == "invalid" {
					auth.ProxyURL = "http://synthetic:must-not-appear@%zz"
				}
				if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				host.inner.RegisterPluginForTest("route-plugin", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: quotaRoute804{endpoint: origin.URL}}})
				trap := &quotaClientTrap804{}
				fetch := pluginapi.QuotaFetchRequest{Provider: "codex", AuthID: auth.ID, HTTPClient: trap}
				reset := pluginapi.QuotaResetRequest{Provider: "codex", AuthID: auth.ID, HTTPClient: trap}
				var body string
				var handled bool
				var errCall error
				if strings.HasPrefix(operation, "fetch") {
					var result pluginapi.QuotaFetchResponse
					if operation == "fetch" {
						result, handled, errCall = host.FetchQuota(t.Context(), fetch)
					} else {
						result, handled, errCall = host.FetchQuotaByPlugin(t.Context(), "route-plugin", fetch)
					}
					if result.Subscription != nil {
						body = result.Subscription.Plan
					}
				} else {
					var result pluginapi.QuotaResetResponse
					if operation == "reset" {
						result, handled, errCall = host.ResetQuota(t.Context(), reset)
					} else {
						result, handled, errCall = host.ResetQuotaByPlugin(t.Context(), "route-plugin", reset)
					}
					body = result.Message
				}
				if !handled || globalHits.Load() != 0 || trap.calls.Load() != 0 {
					t.Fatalf("handled=%v global=%d unbound=%d", handled, globalHits.Load(), trap.calls.Load())
				}
				if route == "invalid" {
					if errCall == nil || strings.Contains(errCall.Error(), "must-not-appear") || proxyHits.Load()+originHits.Load() != 0 {
						t.Fatal("invalid credential proxy did not fail closed with redacted error")
					}
				} else if errCall != nil || body != route || proxyHits.Load()+originHits.Load() != 1 {
					t.Fatalf("quota route=%q error=%v proxy=%d origin=%d", body, errCall, proxyHits.Load(), originHits.Load())
				}
			})
		}
	}
}
