package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

func TestAntigravityExecutionProxyCannotReplaceCredentialCacheRoute(t *testing.T) {
	var credentialHits, requestHits atomic.Int32
	credential := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credentialHits.Add(1)
		if r.ProtoMajor != 1 {
			t.Error("Antigravity lost HTTP/1.1")
		}
		_, _ = io.WriteString(w, "credential")
	}))
	defer credential.Close()
	request := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requestHits.Add(1); _, _ = io.WriteString(w, "request") }))
	defer request.Close()
	auth := &coreauth.Auth{ID: "antigravity-bound", Provider: "antigravity", ProxyURL: credential.URL}
	for _, requested := range []string{credential.URL, request.URL, credential.URL} {
		ctx := coreexecutor.WithRequestProxyURL(context.Background(), requested)
		client := newAntigravityHTTPClient(ctx, nil, auth, 0)
		response, errCall := client.Get("http://upstream.example/v1")
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		if requested == request.URL {
			var policy *proxyutil.ExecutionProxyError
			if !errors.As(errCall, &policy) {
				t.Fatalf("expected typed policy rejection: %v", errCall)
			}
		} else if errCall != nil {
			t.Fatal(errCall)
		}
	}
	if credentialHits.Load() != 2 || requestHits.Load() != 0 {
		t.Fatalf("route counts credential=%d request=%d", credentialHits.Load(), requestHits.Load())
	}
	unbound := &coreauth.Auth{ID: "antigravity-unbound", Provider: "antigravity"}
	ctx := coreexecutor.WithRequestProxyURL(context.Background(), request.URL)
	response, errCall := newAntigravityHTTPClient(ctx, &config.Config{SDKConfig: config.SDKConfig{ProxyURL: credential.URL}}, unbound, 0).Get("http://upstream.example/v1")
	if errCall != nil {
		t.Fatal(errCall)
	}
	_ = response.Body.Close()
	if requestHits.Load() != 1 {
		t.Fatal("unbound credential did not use request proxy")
	}
}

func TestDirectProviderRefreshIgnoresExecutionProxy(t *testing.T) {
	for _, provider := range []string{"kimi-ai", "antigravity"} {
		t.Run(provider, func(t *testing.T) {
			var credentialHits, requestHits, globalHits atomic.Int32
			fixture := func(hits *atomic.Int32) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					http.Error(w, "controlled OAuth CONNECT rejection", 502)
				}))
			}
			credential, request, global := fixture(&credentialHits), fixture(&requestHits), fixture(&globalHits)
			defer credential.Close()
			defer request.Close()
			defer global.Close()
			auth := &coreauth.Auth{ID: "refresh-" + provider, Provider: provider, ProxyURL: credential.URL, Metadata: map[string]any{"refresh_token": "fixture-refresh", "type": provider}}
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL}}
			ctx := coreexecutor.WithRequestProxyURL(context.Background(), request.URL)
			var errRefresh error
			if provider == "kimi-ai" {
				_, errRefresh = NewKimiExecutor(cfg).Refresh(ctx, auth)
			} else {
				_, errRefresh = NewAntigravityExecutor(cfg).Refresh(ctx, auth)
			}
			if errRefresh == nil {
				t.Fatal("fixture unexpectedly accepted refresh")
			}
			var policy *proxyutil.ExecutionProxyError
			if errors.As(errRefresh, &policy) {
				t.Fatal("refresh must strip request proxy instead of treating it as an override")
			}
			if credentialHits.Load() != 1 || requestHits.Load() != 0 || globalHits.Load() != 0 {
				t.Fatalf("refresh route credential=%d request=%d global=%d", credentialHits.Load(), requestHits.Load(), globalHits.Load())
			}
		})
	}
}

func TestXAIExecutionProxyConflictDoesNotReuseOrCloseLiveWebsocket(t *testing.T) {
	var handshakes, conflictingHits atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		handshakes.Add(1)
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()
	conflicting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { conflictingHits.Add(1); w.WriteHeader(502) }))
	defer conflicting.Close()
	exec := NewXAIWebsocketsExecutor(&config.Config{})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	const sessionID = "xai-bound-route"
	session := exec.getOrCreateSession(sessionID)
	defer exec.CloseExecutionSession(sessionID)
	auth := &coreauth.Auth{ID: "xai-bound", Provider: "xai", ProxyURL: "direct"}
	wsURL := strings.Replace(upstream.URL, "http", "ws", 1)
	conn, _, _, errDial := exec.ensureUpstreamConn(context.Background(), auth, session, auth.ID, wsURL, http.Header{})
	if errDial != nil || conn == nil {
		t.Fatalf("initial connection: %v", errDial)
	}
	ctx := coreexecutor.WithRequestProxyURL(context.Background(), conflicting.URL)
	blocked, _, _, errBlocked := exec.ensureUpstreamConn(ctx, auth, session, auth.ID, wsURL, http.Header{})
	var policy *proxyutil.ExecutionProxyError
	if blocked != nil || !errors.As(errBlocked, &policy) {
		t.Fatalf("conflicting proxy used cached socket: %v", errBlocked)
	}
	reused, _, _, errReuse := exec.ensureUpstreamConn(context.Background(), auth, session, auth.ID, wsURL, http.Header{})
	if errReuse != nil || reused != conn {
		t.Fatalf("rejected request invalidated existing credential route: %v", errReuse)
	}
	if handshakes.Load() != 1 || conflictingHits.Load() != 0 {
		t.Fatalf("unexpected handshakes=%d conflicting=%d", handshakes.Load(), conflictingHits.Load())
	}
}
