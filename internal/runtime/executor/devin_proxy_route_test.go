package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type devinProxyRouteRequest struct {
	url           string
	authorization string
	absolute      bool
	encoding      string
}

type devinProxyRouteEndpoint struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []devinProxyRouteRequest
}

func newDevinProxyRouteEndpoint(t *testing.T, marker string) *devinProxyRouteEndpoint {
	t.Helper()
	endpoint := &devinProxyRouteEndpoint{}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		endpoint.mu.Lock()
		endpoint.requests = append(endpoint.requests, devinProxyRouteRequest{
			url:           r.URL.String(),
			authorization: r.Header.Get("Authorization"),
			absolute:      r.URL.IsAbs(),
			encoding:      r.Header.Get("Accept-Encoding"),
		})
		endpoint.mu.Unlock()
		w.Header().Set("X-Devin-Test-Route", marker)
		switch r.URL.Path {
		case devinauth.DevinGetUserStatusPath:
			w.Header().Set("Content-Type", "application/proto")
			user := appendDevinFieldBytes(nil, 7, []byte(marker+"@example.com"))
			_, _ = w.Write(appendDevinFieldBytes(nil, 1, user))
		case helps.DevinChatPath:
			w.Header().Set("Content-Type", "application/connect+proto")
			frame := appendDevinFieldBytes(nil, 1, []byte("route-response"))
			frame = appendDevinFieldBytes(frame, 3, []byte(marker))
			_, _ = w.Write(helps.WrapConnectEnvelope(frame))
			_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
		default:
			_, _ = io.WriteString(w, marker)
		}
	}))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func (e *devinProxyRouteEndpoint) snapshot() []devinProxyRouteRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]devinProxyRouteRequest(nil), e.requests...)
}

type devinProxyRouteCanary struct {
	calls atomic.Int64
}

func (c *devinProxyRouteCanary) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, fmt.Errorf("unexpected context transport request")
}

func devinProxyRouteAuth(baseURL, proxyURL, token string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       token + ".json",
		Provider: "devin",
		ProxyURL: proxyURL,
		Attributes: map[string]string{
			"api_key":  token,
			"base_url": baseURL,
		},
	}
}

func invokeDevinProxyRoute(ctx context.Context, executor *DevinExecutor, auth *cliproxyauth.Auth, operation, marker string) error {
	request := cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions}
	var body []byte
	var headers http.Header
	switch operation {
	case "http":
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, auth.Attributes["base_url"]+"/probe", nil)
		if errReq != nil {
			return errReq
		}
		resp, errDo := executor.HttpRequest(ctx, auth, req)
		if errDo != nil {
			return errDo
		}
		defer func() { _ = resp.Body.Close() }()
		var errRead error
		body, errRead = io.ReadAll(resp.Body)
		if errRead != nil {
			return errRead
		}
		headers = resp.Header
	case "execute":
		resp, errExecute := executor.Execute(ctx, auth, request, opts)
		if errExecute != nil {
			return errExecute
		}
		body, headers = resp.Payload, resp.Headers
	case "stream":
		opts.Stream = true
		result, errStream := executor.ExecuteStream(ctx, auth, request, opts)
		if errStream != nil {
			return errStream
		}
		headers = result.Headers
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
			body = append(body, chunk.Payload...)
		}
	case "refresh":
		updated, errRefresh := executor.Refresh(ctx, auth)
		if errRefresh != nil {
			return errRefresh
		}
		if updated.ProxyURL != auth.ProxyURL {
			return fmt.Errorf("refresh changed credential proxy from %q to %q", auth.ProxyURL, updated.ProxyURL)
		}
		if updated.Metadata["email"] != marker+"@example.com" {
			return fmt.Errorf("refresh used wrong route: email=%v", updated.Metadata["email"])
		}
		return nil
	default:
		return fmt.Errorf("unknown test operation %q", operation)
	}
	if headers.Get("X-Devin-Test-Route") != marker || !strings.Contains(string(body), marker) {
		return fmt.Errorf("wrong response route: header=%q body=%q", headers.Get("X-Devin-Test-Route"), body)
	}
	return nil
}

func TestDevinProxyCredentialRoutesRemainIsolated(t *testing.T) {
	for _, operation := range []string{"http", "execute", "stream", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			direct := newDevinProxyRouteEndpoint(t, "unexpected-direct")
			global := newDevinProxyRouteEndpoint(t, "unexpected-global")
			proxyA := newDevinProxyRouteEndpoint(t, "route-a")
			proxyB := newDevinProxyRouteEndpoint(t, "route-b")
			canary := &devinProxyRouteCanary{}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", canary)
			cfg := &config.Config{}
			cfg.ProxyURL = global.server.URL
			executor := NewDevinExecutor(cfg)
			errResults := make(chan error, 2)
			for _, route := range []struct {
				proxy  *devinProxyRouteEndpoint
				marker string
			}{{proxyA, "route-a"}, {proxyB, "route-b"}} {
				go func() {
					auth := devinProxyRouteAuth(direct.server.URL, route.proxy.server.URL, route.marker)
					for range 3 {
						if err := invokeDevinProxyRoute(ctx, executor, auth, operation, route.marker); err != nil {
							errResults <- err
							return
						}
					}
					errResults <- nil
				}()
			}
			for range 2 {
				if err := <-errResults; err != nil {
					t.Error(err)
				}
			}
			for _, route := range []struct {
				proxy *devinProxyRouteEndpoint
				token string
			}{{proxyA, "route-a"}, {proxyB, "route-b"}} {
				requests := route.proxy.snapshot()
				if len(requests) != 3 {
					t.Errorf("proxy %s received %d requests, want 3", route.token, len(requests))
				}
				for _, request := range requests {
					if !request.absolute || !strings.HasPrefix(request.url, direct.server.URL+"/") {
						t.Errorf("proxy did not receive intended absolute upstream URL: %+v", request)
					}
					if request.authorization != "Basic "+route.token+"-"+route.token {
						t.Errorf("credential crossed proxy boundary: %+v", request)
					}
					if strings.Contains(strings.ToLower(request.encoding), "gzip") {
						t.Errorf("Devin unexpectedly enabled gzip: %q", request.encoding)
					}
				}
			}
			if len(direct.snapshot()) != 0 || len(global.snapshot()) != 0 || canary.calls.Load() != 0 {
				t.Fatalf("selected credential proxy was bypassed: direct=%d global=%d context=%d", len(direct.snapshot()), len(global.snapshot()), canary.calls.Load())
			}
		})
	}
}

func TestDevinProxySelectedRouteDoesNotFallBack(t *testing.T) {
	for _, operation := range []string{"http", "execute", "stream", "refresh"} {
		for _, selected := range []string{"malformed-credential", "malformed-global", "unavailable-credential"} {
			t.Run(operation+"/"+selected, func(t *testing.T) {
				direct := newDevinProxyRouteEndpoint(t, "unexpected-direct")
				global := newDevinProxyRouteEndpoint(t, "unexpected-global")
				canary := &devinProxyRouteCanary{}
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", canary)
				cfg := &config.Config{}
				cfg.ProxyURL = global.server.URL
				auth := devinProxyRouteAuth(direct.server.URL, "socks5://", "route-token")
				var failedProxyCalls atomic.Int64
				switch selected {
				case "malformed-global":
					auth.ProxyURL, cfg.ProxyURL = "", "socks5://"
				case "unavailable-credential":
					failedProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						failedProxyCalls.Add(1)
						conn, _, errHijack := w.(http.Hijacker).Hijack()
						if errHijack != nil {
							t.Errorf("hijack failed proxy connection: %v", errHijack)
							return
						}
						if errClose := conn.Close(); errClose != nil {
							t.Errorf("close failed proxy connection: %v", errClose)
						}
					}))
					t.Cleanup(failedProxy.Close)
					auth.ProxyURL = failedProxy.URL
				}
				if err := invokeDevinProxyRoute(ctx, NewDevinExecutor(cfg), auth, operation, "unused"); err == nil {
					t.Fatal("expected selected proxy failure")
				}
				if selected == "unavailable-credential" && failedProxyCalls.Load() == 0 {
					t.Fatal("request did not reach the selected failing proxy")
				}
				if len(direct.snapshot()) != 0 || len(global.snapshot()) != 0 || canary.calls.Load() != 0 {
					t.Fatalf("failed selected route fell back: direct=%d global=%d context=%d", len(direct.snapshot()), len(global.snapshot()), canary.calls.Load())
				}
			})
		}
	}
}

func TestDevinProxyExplicitDirectAndGlobalRoutes(t *testing.T) {
	for _, operation := range []string{"http", "execute", "stream", "refresh"} {
		for _, explicitDirect := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/direct=%v", operation, explicitDirect), func(t *testing.T) {
				direct := newDevinProxyRouteEndpoint(t, "direct-route")
				global := newDevinProxyRouteEndpoint(t, "global-route")
				canary := &devinProxyRouteCanary{}
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", canary)
				cfg := &config.Config{}
				cfg.ProxyURL = global.server.URL
				auth := devinProxyRouteAuth(direct.server.URL, "", "route-token")
				marker := "global-route"
				if explicitDirect {
					auth.ProxyURL = "direct"
					marker = "direct-route"
				}
				if err := invokeDevinProxyRoute(ctx, NewDevinExecutor(cfg), auth, operation, marker); err != nil {
					t.Fatal(err)
				}
				wantDirect, wantGlobal := 0, 1
				if explicitDirect {
					wantDirect, wantGlobal = 1, 0
				}
				if len(direct.snapshot()) != wantDirect || len(global.snapshot()) != wantGlobal || canary.calls.Load() != 0 {
					t.Fatalf("route precedence violated: direct=%d global=%d context=%d", len(direct.snapshot()), len(global.snapshot()), canary.calls.Load())
				}
			})
		}
	}
}
