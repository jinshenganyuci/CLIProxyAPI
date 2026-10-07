package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type quotaProxyRPCClient struct {
	call func(context.Context, string, []byte) ([]byte, error)
}

func (c quotaProxyRPCClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	return c.call(ctx, method, request)
}

func (quotaProxyRPCClient) Shutdown() {}

type quotaProxyHTTPClient struct {
	authID   string
	mu       sync.Mutex
	contexts []context.Context
}

func (c *quotaProxyHTTPClient) record(ctx context.Context) {
	c.mu.Lock()
	c.contexts = append(c.contexts, ctx)
	c.mu.Unlock()
}

func (c *quotaProxyHTTPClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.record(ctx)
	return pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(c.authID)}, nil
}

func (c *quotaProxyHTTPClient) DoStream(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	c.record(ctx)
	chunks := make(chan pluginapi.HTTPStreamChunk, 1)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(c.authID)}
	return pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: chunks}, nil
}

func TestRPCQuotaCallbacksKeepConcurrentCredentialClients(t *testing.T) {
	for _, method := range []string{pluginabi.MethodQuotaFetch, pluginabi.MethodQuotaReset} {
		t.Run(method, func(t *testing.T) {
			host := New()
			var fallbackCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackCalls.Add(1)
				_, _ = w.Write([]byte("unbound-global-route"))
			}))
			defer server.Close()
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			var scopesMu sync.Mutex
			var callbackIDs, streamIDs []string
			client := quotaProxyRPCClient{call: func(ctx context.Context, gotMethod string, raw []byte) ([]byte, error) {
				var req rpcQuotaFetchRequest
				if err := json.Unmarshal(raw, &req); err != nil {
					return nil, err
				}
				entered <- struct{}{}
				<-release
				if gotMethod != method || req.HostCallbackID == "" {
					return nil, fmt.Errorf("invalid quota callback method or missing scope")
				}
				scopesMu.Lock()
				callbackIDs = append(callbackIDs, req.HostCallbackID)
				scopesMu.Unlock()
				callbackReq, err := json.Marshal(rpcHostHTTPRequest{HostCallbackID: req.HostCallbackID, Method: http.MethodGet, URL: server.URL})
				if err != nil {
					return nil, err
				}
				rawResponse, err := host.callFromPlugin(withHostCallbackPluginID(context.Background(), "quota-plugin"), pluginabi.MethodHostHTTPDo, callbackReq)
				if err != nil {
					return nil, err
				}
				response, err := decodeRPCEnvelope[pluginapi.HTTPResponse](rawResponse)
				if err != nil {
					return nil, err
				}
				if string(response.Body) != req.AuthID {
					return nil, fmt.Errorf("HTTP route = %q, want credential %q", response.Body, req.AuthID)
				}
				rawStream, err := host.callFromPlugin(withHostCallbackPluginID(context.Background(), "quota-plugin"), pluginabi.MethodHostHTTPDoStream, callbackReq)
				if err != nil {
					return nil, err
				}
				stream, err := decodeRPCEnvelope[rpcHostHTTPStreamResponse](rawStream)
				if err != nil {
					return nil, err
				}
				scopesMu.Lock()
				streamIDs = append(streamIDs, stream.StreamID)
				scopesMu.Unlock()
				chunk, done, err := host.httpStreams.read(context.Background(), "quota-plugin", nil, stream.StreamID)
				if err != nil || done || string(chunk.Payload) != req.AuthID {
					return nil, fmt.Errorf("stream route = %q, want credential %q; done=%v, err=%v", chunk.Payload, req.AuthID, done, err)
				}
				return marshalRPCResult(pluginapi.QuotaResetResponse{Success: true})
			}}
			provider := rpcQuotaProvider{rpcPluginAdapter: &rpcPluginAdapter{id: "quota-plugin", host: host, client: client}}
			clients := []*quotaProxyHTTPClient{{authID: "credential-a"}, {authID: "credential-b"}}
			done := make(chan error, len(clients))
			for _, client := range clients {
				go func() {
					var err error
					if method == pluginabi.MethodQuotaFetch {
						_, err = provider.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{AuthID: client.authID, HTTPClient: client})
					} else {
						_, err = provider.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{AuthID: client.authID, HTTPClient: client})
					}
					done <- err
				}()
			}
			for range clients {
				<-entered
			}
			close(release)
			for range clients {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
			if fallbackCalls.Load() != 0 {
				t.Errorf("quota callbacks used unbound route %d times", fallbackCalls.Load())
			}
			for _, client := range clients {
				if len(client.contexts) != 2 {
					t.Errorf("%s client calls = %d, want HTTP and stream", client.authID, len(client.contexts))
				}
				for _, ctx := range client.contexts {
					if ctx.Err() != context.Canceled {
						t.Errorf("completed quota scope context error = %v, want canceled", ctx.Err())
					}
				}
			}
			if len(callbackIDs) == 2 && callbackIDs[0] == callbackIDs[1] {
				t.Error("concurrent credentials shared callback scope")
			}
			for _, id := range callbackIDs {
				if callbackContextExists(host, id) {
					t.Error("completed quota scope remains registered")
				}
				for _, callbackMethod := range []string{pluginabi.MethodHostHTTPDo, pluginabi.MethodHostHTTPDoStream} {
					raw, _ := json.Marshal(rpcHostHTTPRequest{HostCallbackID: id, Method: http.MethodGet, URL: server.URL})
					if _, err := host.callFromPlugin(context.Background(), callbackMethod, raw); err == nil {
						t.Errorf("expired quota scope allowed %s", callbackMethod)
					}
				}
			}
			for _, id := range streamIDs {
				// Context cancellation removes the operation before its asynchronous
				// cleanup closes the stream. Observe the actual cleanup boundary.
				deadline := time.Now().Add(3 * time.Second)
				for {
					host.httpStreams.mu.Lock()
					_, exists := host.httpStreams.streams[hostHTTPStreamKey{pluginID: "quota-plugin", streamID: id}]
					host.httpStreams.mu.Unlock()
					if !exists {
						break
					}
					if !time.Now().Before(deadline) {
						t.Error("completed quota scope retained stream")
						break
					}
					runtime.Gosched()
				}
			}
			if fallbackCalls.Load() != 0 {
				t.Errorf("expired callbacks used unbound route %d times", fallbackCalls.Load())
			}
		})
	}
}

func TestRPCQuotaCallbacksRejectInvalidScopes(t *testing.T) {
	for _, scenario := range []string{"unknown", "canceled", "another-plugin", "missing-caller"} {
		t.Run(scenario, func(t *testing.T) {
			host := New()
			var fallbackCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			client := &quotaProxyHTTPClient{authID: "credential"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var callbackID string
			rpcClient := quotaProxyRPCClient{call: func(_ context.Context, _ string, raw []byte) ([]byte, error) {
				var req rpcQuotaFetchRequest
				if err := json.Unmarshal(raw, &req); err != nil {
					return nil, err
				}
				callbackID = req.HostCallbackID
				requestID := callbackID
				caller := withHostCallbackPluginID(context.Background(), "quota-plugin")
				switch scenario {
				case "unknown":
					requestID = "unknown-callback"
				case "canceled":
					cancel()
				case "another-plugin":
					caller = withHostCallbackPluginID(caller, "different-plugin")
				case "missing-caller":
					caller = context.Background()
				}
				request, err := json.Marshal(rpcHostHTTPRequest{HostCallbackID: requestID, Method: http.MethodGet, URL: server.URL})
				if err != nil {
					return nil, err
				}
				for _, callbackMethod := range []string{pluginabi.MethodHostHTTPDo, pluginabi.MethodHostHTTPDoStream} {
					if _, err := host.callFromPlugin(caller, callbackMethod, request); err == nil {
						t.Errorf("%s allowed %s callback", scenario, callbackMethod)
					}
				}
				return nil, fmt.Errorf("quota plugin failed")
			}}
			provider := rpcQuotaProvider{rpcPluginAdapter: &rpcPluginAdapter{id: "quota-plugin", host: host, client: rpcClient}}
			if _, err := provider.FetchQuota(ctx, pluginapi.QuotaFetchRequest{HTTPClient: client}); err == nil {
				t.Fatal("quota plugin error was not propagated")
			}
			if fallbackCalls.Load() != 0 || len(client.contexts) != 0 {
				t.Errorf("invalid scope made HTTP calls: fallback=%d, credential=%d", fallbackCalls.Load(), len(client.contexts))
			}
			if callbackContextExists(host, callbackID) {
				t.Error("failed quota scope remains registered")
			}
		})
	}
}

func TestRPCQuotaRequiresBoundClientAndCallbackRegistry(t *testing.T) {
	for _, method := range []string{pluginabi.MethodQuotaFetch, pluginabi.MethodQuotaReset} {
		for _, missing := range []string{"client", "registry"} {
			t.Run(method+"/"+missing, func(t *testing.T) {
				host := New()
				var httpClient pluginapi.HostHTTPClient = &quotaProxyHTTPClient{authID: "credential"}
				if missing == "client" {
					httpClient = nil
				} else {
					host.callbackContexts = nil
				}
				called := false
				provider := rpcQuotaProvider{rpcPluginAdapter: &rpcPluginAdapter{host: host, client: quotaProxyRPCClient{call: func(context.Context, string, []byte) ([]byte, error) {
					called = true
					return marshalRPCResult(pluginapi.QuotaResetResponse{Success: true})
				}}}}
				var err error
				if method == pluginabi.MethodQuotaFetch {
					_, err = provider.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{HTTPClient: httpClient})
				} else {
					_, err = provider.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{HTTPClient: httpClient})
				}
				if err == nil || called {
					t.Errorf("quota request missing %s: err=%v, plugin called=%v", missing, err, called)
				}
			})
		}
	}
}

func TestRPCQuotaPluginCannotOmitOrBorrowCallbackScope(t *testing.T) {
	host := New()
	host.RegisterPluginForTest("quota-plugin", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: &testQuotaProvider{}}})
	otherID, closeOther := host.openCallbackContextForPlugin(context.Background(), "other-plugin")
	defer closeOther()
	var fallbackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		_, _ = w.Write([]byte("legacy-response"))
	}))
	defer server.Close()
	for _, callbackID := range []string{"", otherID} {
		for _, method := range []string{pluginabi.MethodHostHTTPDo, pluginabi.MethodHostHTTPDoStream} {
			raw, err := json.Marshal(rpcHostHTTPRequest{HostCallbackID: callbackID, Method: http.MethodGet, URL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := host.callFromPlugin(withHostCallbackPluginID(context.Background(), "quota-plugin"), method, raw); err == nil {
				t.Errorf("quota plugin was allowed %s with omitted or foreign scope %q", method, callbackID)
			}
		}
	}
	if fallbackCalls.Load() != 0 {
		t.Errorf("invalid quota callbacks made %d global calls", fallbackCalls.Load())
	}
	for _, method := range []string{pluginabi.MethodHostHTTPDo, pluginabi.MethodHostHTTPDoStream} {
		raw, err := json.Marshal(rpcHostHTTPRequest{Method: http.MethodGet, URL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		response, err := host.callFromPlugin(withHostCallbackPluginID(context.Background(), "legacy-plugin"), method, raw)
		if err != nil {
			t.Fatalf("legacy callback %s error: %v", method, err)
		}
		if method == pluginabi.MethodHostHTTPDoStream {
			stream, err := decodeRPCEnvelope[rpcHostHTTPStreamResponse](response)
			if err != nil {
				t.Fatal(err)
			}
			host.httpStreams.close("quota-plugin", nil, stream.StreamID)
		}
	}
}

func TestRPCQuotaDetachedPluginStillRequiresCallbackScope(t *testing.T) {
	host := New()
	host.RegisterPluginForTest("quota-plugin", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: &testQuotaProvider{}}})
	var fallbackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	assertUnscopedRejected := func() {
		t.Helper()
		raw, err := json.Marshal(rpcHostHTTPRequest{Method: http.MethodGet, URL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{pluginabi.MethodHostHTTPDo, pluginabi.MethodHostHTTPDoStream} {
			if _, err := host.callFromPlugin(withHostCallbackPluginID(context.Background(), "quota-plugin"), method, raw); err == nil {
				t.Errorf("detached quota plugin allowed %s without scope", method)
			}
		}
	}
	var callbackID string
	rpcClient := quotaProxyRPCClient{call: func(_ context.Context, _ string, raw []byte) ([]byte, error) {
		var req rpcQuotaFetchRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		callbackID = req.HostCallbackID
		// Unload removes the active snapshot before waiting for in-flight calls.
		host.snapshot.Store(emptySnapshot())
		assertUnscopedRejected()
		return marshalRPCResult(pluginapi.QuotaFetchResponse{})
	}}
	provider := rpcQuotaProvider{rpcPluginAdapter: &rpcPluginAdapter{id: "quota-plugin", host: host, client: rpcClient}}
	if _, err := provider.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{HTTPClient: &quotaProxyHTTPClient{authID: "credential"}}); err != nil {
		t.Fatal(err)
	}
	if callbackContextExists(host, callbackID) {
		t.Fatal("completed quota scope remains registered")
	}
	assertUnscopedRejected()
	// Reusing an ID for a non-quota replacement retains the stricter policy.
	host.RegisterPluginForTest("quota-plugin", pluginapi.Plugin{})
	assertUnscopedRejected()
	if fallbackCalls.Load() != 0 {
		t.Errorf("detached quota callbacks made %d global calls", fallbackCalls.Load())
	}
}
