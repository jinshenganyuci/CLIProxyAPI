package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestMetaHTTPClientRoutePrecedence(t *testing.T) {
	var directHits, credentialHits, globalHits, contextHits, defaultHits atomic.Int64
	direct := metaHTTPRouteServer(t, "direct", &directHits)
	credential := metaHTTPRouteServer(t, "credential", &credentialHits)
	global := metaHTTPRouteServer(t, "global", &globalHits)
	injected := metaHTTPRouteServer(t, "context", &contextHits)
	fallback := metaHTTPRouteServer(t, "default", &defaultHits)

	base := http.DefaultTransport
	defaultTransport := metaHTTPProxyTransport(t, fallback.URL)
	http.DefaultTransport = defaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = base
		defaultTransport.CloseIdleConnections()
	})
	contextTransport := metaHTTPProxyTransport(t, injected.URL)
	t.Cleanup(contextTransport.CloseIdleConnections)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", contextTransport)
	for _, tc := range []struct {
		name       string
		credential string
		global     string
		ctx        context.Context
		want       string
	}{
		{name: "credential over global context and defaults", credential: "  " + credential.URL + "  ", global: global.URL, ctx: ctx, want: "credential"},
		{name: "credential ignores invalid global", credential: credential.URL, global: "invalid-global", ctx: ctx, want: "credential"},
		{name: "global over context and defaults", global: "  " + global.URL + "  ", ctx: ctx, want: "global"},
		{name: "blank credential inherits global", credential: " \t ", global: global.URL, ctx: ctx, want: "global"},
		{name: "unset routes use injected transport", ctx: ctx, want: "context"},
		{name: "unset routes use process defaults", ctx: context.Background(), want: "default"},
		{name: "nil context uses process defaults", want: "default"},
		{name: "credential direct overrides all routes", credential: "direct", global: global.URL, ctx: ctx, want: "direct"},
		{name: "global direct overrides context and defaults", global: "none", ctx: ctx, want: "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counters := map[string]*atomic.Int64{
				"direct": &directHits, "credential": &credentialHits, "global": &globalHits,
				"context": &contextHits, "default": &defaultHits,
			}
			before := make(map[string]int64, len(counters))
			for route, counter := range counters {
				before[route] = counter.Load()
			}
			cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: tc.global}}
			client, errClient := NewMetaHTTPClient(tc.ctx, cfg, &cliproxyauth.Auth{ProxyURL: tc.credential}, 0)
			if errClient != nil {
				t.Fatal(errClient)
			}
			if client.Timeout != 0 {
				t.Fatalf("inference client timeout = %v, want zero", client.Timeout)
			}
			metaHTTPRequireRoute(t, client, direct.URL+"/inference", tc.want)
			for route, counter := range counters {
				want := int64(0)
				if route == tc.want {
					want = 1
				}
				if got := counter.Load() - before[route]; got != want {
					t.Errorf("%s received %d requests, want %d", route, got, want)
				}
			}
		})
	}
}

func TestMetaHTTPClientExplicitDirectBypassesEnvironmentAndDefaultProxy(t *testing.T) {
	var directHits, proxyHits, proxyLookups atomic.Int64
	direct := metaHTTPRouteServer(t, "direct", &directHits)
	proxy := metaHTTPRouteServer(t, "proxy", &proxyHits)
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	base := http.DefaultTransport
	proxyURL, errURL := url.Parse(proxy.URL)
	if errURL != nil {
		t.Fatal(errURL)
	}
	defaultTransport := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) {
		proxyLookups.Add(1)
		return proxyURL, nil
	}}
	http.DefaultTransport = defaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = base
		defaultTransport.CloseIdleConnections()
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", defaultTransport)
	for _, route := range []string{"direct", "none", " DIRECT ", " NoNe "} {
		t.Run(strings.TrimSpace(route), func(t *testing.T) {
			client, errClient := NewMetaHTTPClient(ctx, &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxy.URL}}, &cliproxyauth.Auth{ProxyURL: route}, 0)
			if errClient != nil {
				t.Fatal(errClient)
			}
			transport, ok := client.Transport.(*http.Transport)
			if !ok || transport.Proxy != nil {
				t.Fatalf("direct route retained proxy resolver: %T", client.Transport)
			}
			metaHTTPRequireRoute(t, client, direct.URL, "direct")
		})
	}
	if directHits.Load() != 4 || proxyHits.Load() != 0 || proxyLookups.Load() != 0 {
		t.Fatalf("unexpected route counts: direct=%d proxy=%d resolver=%d", directHits.Load(), proxyHits.Load(), proxyLookups.Load())
	}
}

func TestMetaHTTPClientInvalidSelectedRouteNeverFallsThrough(t *testing.T) {
	var fallbackHits atomic.Int64
	fallback := metaHTTPRouteServer(t, "fallback", &fallbackHits)
	transport := metaHTTPProxyTransport(t, fallback.URL)
	t.Cleanup(transport.CloseIdleConnections)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "missing scheme", raw: "proxy.invalid:1080"},
		{name: "unsupported scheme", raw: "ftp://proxy.invalid:1080"},
		{name: "missing host with credentials", raw: "http://user:private-password@"},
		{name: "invalid escape", raw: "http://%invalid"},
	} {
		for _, source := range []string{"credential", "global"} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: fallback.URL}}
				auth := &cliproxyauth.Auth{}
				if source == "credential" {
					auth.ProxyURL = tc.raw
				} else {
					cfg.ProxyURL = tc.raw
				}
				client, errClient := NewMetaHTTPClient(ctx, cfg, auth, 0)
				if errClient == nil || client != nil {
					t.Fatalf("invalid selected proxy produced client %v, error %v", client, errClient)
				}
				if strings.Contains(errClient.Error(), "private-password") || strings.Contains(errClient.Error(), "user:") {
					t.Fatal("invalid proxy error disclosed credentials")
				}
			})
		}
	}
	if fallbackHits.Load() != 0 {
		t.Fatalf("invalid route made %d fallback requests", fallbackHits.Load())
	}
}

func TestMetaHTTPClientUnavailableSelectedProxyNeverFallsThrough(t *testing.T) {
	var directHits, fallbackHits atomic.Int64
	direct := metaHTTPRouteServer(t, "direct", &directHits)
	fallback := metaHTTPRouteServer(t, "fallback", &fallbackHits)
	unavailable := httptest.NewServer(http.NotFoundHandler())
	selectedURL := unavailable.URL
	unavailable.Close()
	transport := metaHTTPProxyTransport(t, fallback.URL)
	t.Cleanup(transport.CloseIdleConnections)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
	client, errClient := NewMetaHTTPClient(ctx, &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: fallback.URL}}, &cliproxyauth.Auth{ProxyURL: selectedURL}, 0)
	if errClient != nil {
		t.Fatal(errClient)
	}
	response, errRequest := client.Get(direct.URL)
	if response != nil {
		if errClose := response.Body.Close(); errClose != nil {
			t.Errorf("close unexpected response: %v", errClose)
		}
	}
	if errRequest == nil {
		t.Fatal("unavailable credential proxy unexpectedly succeeded")
	}
	if directHits.Load() != 0 || fallbackHits.Load() != 0 {
		t.Fatalf("proxy connection failure fell through: direct=%d fallback=%d", directHits.Load(), fallbackHits.Load())
	}
}

func TestMetaHTTPClientReusesSelectedProxyConnectionWithoutInferenceTimeout(t *testing.T) {
	var mu sync.Mutex
	connections := make(map[string]int)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		connections[r.RemoteAddr]++
		mu.Unlock()
		_, _ = io.WriteString(w, "proxy")
	}))
	t.Cleanup(proxy.Close)
	first, errFirst := NewMetaHTTPClient(nil, nil, &cliproxyauth.Auth{ProxyURL: " " + proxy.URL + " "}, 0)
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	second, errSecond := NewMetaHTTPClient(nil, &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxy.URL}}, nil, 30*time.Second)
	if errSecond != nil {
		t.Fatal(errSecond)
	}
	if first.Transport != second.Transport {
		t.Fatal("same selected route did not reuse its HTTP transport")
	}
	if first.Timeout != 0 || second.Timeout != 30*time.Second {
		t.Fatalf("client timeouts: inference=%v acquisition=%v", first.Timeout, second.Timeout)
	}
	transport, ok := first.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != 0 {
		t.Fatal("inference transport has a response header timeout")
	}
	t.Cleanup(transport.CloseIdleConnections)
	metaHTTPRequireRoute(t, first, "http://meta.invalid/first", "proxy")
	metaHTTPRequireRoute(t, second, "http://meta.invalid/second", "proxy")
	mu.Lock()
	defer mu.Unlock()
	if len(connections) != 1 {
		t.Fatalf("two requests used %d proxy TCP connections, want one", len(connections))
	}
	for _, requests := range connections {
		if requests != 2 {
			t.Fatalf("reused proxy connection received %d requests, want two", requests)
		}
	}
}

func metaHTTPRouteServer(t *testing.T, route string, counter *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		counter.Add(1)
		_, _ = io.WriteString(w, route)
	}))
	t.Cleanup(server.Close)
	return server
}

func metaHTTPProxyTransport(t *testing.T, raw string) *http.Transport {
	t.Helper()
	proxyURL, errURL := url.Parse(raw)
	if errURL != nil {
		t.Fatal(errURL)
	}
	return &http.Transport{Proxy: http.ProxyURL(proxyURL)}
}

func metaHTTPRequireRoute(t *testing.T, client *http.Client, target, want string) {
	t.Helper()
	response, errRequest := client.Get(target)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			t.Errorf("close response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(response.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if response.StatusCode != http.StatusOK || string(body) != want {
		t.Fatalf("route response = %d %q, want 200 %q", response.StatusCode, body, want)
	}
}
