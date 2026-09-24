package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostHTTPOperationQuotaProxyLifecycle(t *testing.T) {
	for _, method := range []string{pluginabi.MethodHostHTTPDo, pluginabi.MethodHostHTTPDoStream} {
		for _, scenario := range []string{"proxy", "direct", "invalid_proxy", "cancel", "scope_close", "instance_close"} {
			t.Run(method+"/"+scenario, func(t *testing.T) {
				var proxyHits, directHits, globalHits atomic.Int32
				started, canceled, release := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
				held := scenario == "cancel" || scenario == "scope_close" || scenario == "instance_close"
				respond := func(w http.ResponseWriter, r *http.Request) {
					if held {
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						started <- struct{}{}
						select {
						case <-r.Context().Done():
							canceled <- struct{}{}
						case <-release:
						}
						return
					}
					_, _ = io.WriteString(w, "credential-route")
				}
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { directHits.Add(1); respond(w, r) }))
				t.Cleanup(origin.Close)
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1); respond(w, r) }))
				t.Cleanup(proxy.Close)
				global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					globalHits.Add(1)
					http.Error(w, "wrong global route", 502)
				}))
				t.Cleanup(global.Close)
				t.Cleanup(func() { close(release) })
				host := New()
				host.runtimeConfig = &config.Config{}
				host.runtimeConfig.ProxyURL = global.URL
				auth := &coreauth.Auth{ID: "credential", Provider: "codex", ProxyURL: proxy.URL}
				if scenario == "direct" {
					auth.ProxyURL = "direct"
				} else if scenario == "invalid_proxy" {
					auth.ProxyURL = "http://fixture:must-not-leak@%zz"
				}
				instance := &hostCallbackInstance{}
				caller := withHostCallbackIdentity(context.Background(), "quota-plugin", instance)
				provider := rpcQuotaProvider{rpcPluginAdapter: &rpcPluginAdapter{id: "quota-plugin", host: host, instance: instance}}
				callbackID, closeCallback, errScope := provider.openQuotaCallbackContext(context.Background(), host.newHTTPClient(auth))
				if errScope != nil {
					t.Fatal(errScope)
				}
				defer closeCallback()
				operationID := openHostHTTPOperation(t, host, caller, callbackID)
				raw, _ := json.Marshal(rpcHostHTTPRequest{HostCallbackID: callbackID, OperationID: operationID, Method: http.MethodGet, URL: origin.URL})
				type callResult struct {
					raw []byte
					err error
				}
				done := make(chan callResult, 1)
				go func() { result, err := host.callFromPlugin(caller, method, raw); done <- callResult{result, err} }()
				if held {
					awaitHTTPUpgradeSignal(t, started, "credential proxy request started")
				}
				var result callResult
				if !held || method == pluginabi.MethodHostHTTPDoStream {
					select {
					case result = <-done:
					case <-time.After(2 * time.Second):
						t.Fatal("HTTP operation did not return")
					}
				}
				if held {
					switch scenario {
					case "cancel":
						cancelRaw, _ := json.Marshal(rpcHostHTTPCancelRequest{HostCallbackID: callbackID, OperationID: operationID})
						if _, errCancel := host.callFromPlugin(caller, pluginabi.MethodHostHTTPCancel, cancelRaw); errCancel != nil {
							t.Fatal(errCancel)
						}
					case "scope_close":
						closeCallback()
					case "instance_close":
						host.closeHostHTTPCallbackInstance("quota-plugin", instance)
					}
					awaitHTTPUpgradeSignal(t, canceled, "real upstream cancellation")
					if method == pluginabi.MethodHostHTTPDo {
						select {
						case result = <-done:
						case <-time.After(2 * time.Second):
							t.Fatal("canceled HTTP operation did not return")
						}
						if !errors.Is(result.err, context.Canceled) {
							t.Fatalf("cancellation error = %v", result.err)
						}
					} else if result.err != nil {
						t.Fatal(result.err)
					}
				} else if scenario == "invalid_proxy" {
					if result.err == nil || strings.Contains(result.err.Error(), "must-not-leak") {
						t.Fatalf("invalid proxy error = %v", result.err)
					}
				} else {
					if result.err != nil {
						t.Fatal(result.err)
					}
					if method == pluginabi.MethodHostHTTPDo {
						response, err := decodeRPCEnvelope[pluginapi.HTTPResponse](result.raw)
						if err != nil || string(response.Body) != "credential-route" {
							t.Fatalf("HTTP response = %+v, %v", response, err)
						}
					} else {
						response, err := decodeRPCEnvelope[rpcHostHTTPStreamResponse](result.raw)
						if err != nil {
							t.Fatal(err)
						}
						var body []byte
						for {
							chunk, ended, errRead := host.httpStreams.read(t.Context(), "quota-plugin", instance, response.StreamID)
							if errRead != nil || chunk.Err != nil {
								t.Fatalf("read = %v, %v", errRead, chunk.Err)
							}
							body = append(body, chunk.Payload...)
							if ended {
								break
							}
						}
						if string(body) != "credential-route" {
							t.Fatalf("stream body = %q", body)
						}
					}
				}
				wantProxy, wantDirect := int32(1), int32(0)
				if scenario == "direct" {
					wantProxy, wantDirect = 0, 1
				} else if scenario == "invalid_proxy" {
					wantProxy = 0
				}
				if proxyHits.Load() != wantProxy || directHits.Load() != wantDirect || globalHits.Load() != 0 {
					t.Fatalf("route hits proxy=%d direct=%d global=%d", proxyHits.Load(), directHits.Load(), globalHits.Load())
				}
				host.httpOperations.mu.Lock()
				operations := len(host.httpOperations.operations)
				host.httpOperations.mu.Unlock()
				host.httpStreams.mu.Lock()
				streams := len(host.httpStreams.streams)
				host.httpStreams.mu.Unlock()
				if operations != 0 || streams != 0 {
					t.Fatalf("retained operations=%d streams=%d", operations, streams)
				}
			})
		}
	}
}

func awaitHTTPUpgradeSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("did not observe %s", label)
	}
}

func TestHostHTTPOperationSamePluginDifferentInstanceCannotClaimOrCancel(t *testing.T) {
	host := New()
	owner, foreign := &hostCallbackInstance{}, &hostCallbackInstance{}
	caller := withHostCallbackIdentity(t.Context(), "plugin", owner)
	operationID := openHostHTTPOperation(t, host, caller, "")
	other := withHostCallbackIdentity(t.Context(), "plugin", foreign)
	if _, err := host.acquireHostHTTPOperation(other, "", operationID); err == nil {
		t.Fatal("foreign instance claimed operation")
	}
	raw, _ := json.Marshal(rpcHostHTTPCancelRequest{OperationID: operationID})
	if _, err := host.callFromPlugin(other, pluginabi.MethodHostHTTPCancel, raw); err != nil {
		t.Fatal(err)
	}
	operation, err := host.acquireHostHTTPOperation(caller, "", operationID)
	if err != nil || operation.ctx.Err() != nil {
		t.Fatalf("foreign cancellation affected owner: %v", err)
	}
	operation.finish()
}

func TestHostHTTPOperationWithoutCredentialRetainsNativeGlobalRouting(t *testing.T) {
	for _, route := range []string{"global_proxy", "direct", "invalid_global"} {
		t.Run(route, func(t *testing.T) {
			var directHits, proxyHits atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				directHits.Add(1)
				_, _ = io.WriteString(w, "standalone")
			}))
			defer origin.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { proxyHits.Add(1); _, _ = io.WriteString(w, "standalone") }))
			defer proxy.Close()
			host := New()
			host.runtimeConfig = &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: config.CodexCredentialProxyPolicyRequire, CredentialIdentity: config.CodexCredentialIdentityConfig{Enabled: true}}}
			host.runtimeConfig.ProxyURL = proxy.URL
			if route == "direct" {
				host.runtimeConfig.ProxyURL = "direct"
			} else if route == "invalid_global" {
				host.runtimeConfig.ProxyURL = "invalid-proxy"
			}
			caller := withHostCallbackIdentity(t.Context(), "independent-plugin", &hostCallbackInstance{})
			operationID := openHostHTTPOperation(t, host, caller, "")
			raw, _ := json.Marshal(rpcHostHTTPRequest{OperationID: operationID, URL: origin.URL})
			response, err := host.callFromPlugin(caller, pluginabi.MethodHostHTTPDo, raw)
			if route == "invalid_global" {
				if err == nil || proxyHits.Load()+directHits.Load() != 0 {
					t.Fatalf("invalid global fallback: %v proxy=%d direct=%d", err, proxyHits.Load(), directHits.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := decodeRPCEnvelope[pluginapi.HTTPResponse](response)
			if err != nil || string(parsed.Body) != "standalone" {
				t.Fatalf("independent response %s, %v", response, err)
			}
			if (route == "global_proxy" && (proxyHits.Load() != 1 || directHits.Load() != 0)) || (route == "direct" && (directHits.Load() != 1 || proxyHits.Load() != 0)) {
				t.Fatalf("wrong independent route proxy=%d direct=%d", proxyHits.Load(), directHits.Load())
			}
		})
	}
}
