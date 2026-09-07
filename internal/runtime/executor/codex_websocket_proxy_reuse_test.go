package executor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexWebsocketProxyChangeRebuildsConnection(t *testing.T) {
	upgrader := websocket.Upgrader{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(writer, request, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	var firstHits, secondHits atomic.Int32
	newProxy := func(hits *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodConnect || request.Host != "codex-proxy-test.invalid:80" {
				http.Error(writer, "unexpected target", http.StatusBadRequest)
				return
			}
			hits.Add(1)
			remote, errDial := net.Dial("tcp", strings.TrimPrefix(upstream.URL, "http://"))
			if errDial != nil {
				t.Errorf("dial local upstream: %v", errDial)
				return
			}
			defer func() { _ = remote.Close() }()
			client, buffered, errHijack := writer.(http.Hijacker).Hijack()
			if errHijack != nil {
				t.Errorf("hijack: %v", errHijack)
				return
			}
			defer func() { _ = client.Close() }()
			if _, errWrite := fmt.Fprint(buffered, "HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
				t.Errorf("CONNECT response: %v", errWrite)
				return
			}
			if errFlush := buffered.Flush(); errFlush != nil {
				t.Errorf("flush CONNECT: %v", errFlush)
				return
			}
			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(remote, buffered)
				_ = remote.Close()
				close(done)
			}()
			_, _ = io.Copy(client, remote)
			_ = client.Close()
			<-done
		}))
	}
	firstProxy := newProxy(&firstHits)
	defer firstProxy.Close()
	secondProxy := newProxy(&secondHits)
	defer secondProxy.Close()
	cfg := &config.Config{}
	cfg.ProxyURL = firstProxy.URL
	exec := NewCodexWebsocketsExecutor(cfg)
	exec.store = &codexWebsocketSessionStore{}
	sess := exec.getOrCreateSession(t.Name())
	defer exec.CloseExecutionSession(t.Name())
	auth := &cliproxyauth.Auth{ID: "stable-credential", Provider: "codex", ProxyURL: firstProxy.URL}
	const wsURL = "ws://codex-proxy-test.invalid/responses"
	first, _, _, errFirst := exec.ensureUpstreamConn(context.Background(), auth, sess, auth.ID, wsURL, nil)
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	auth.ProxyURL = secondProxy.URL
	if conn, _ := existingWebsocketSessionConn(sess, auth.ID, wsURL, helps.CodexWebsocketConnectionKey(cfg, auth)); conn != nil {
		t.Fatal("retained connection matched a different proxy")
	}
	second, _, _, errSecond := exec.ensureUpstreamConn(context.Background(), auth, sess, auth.ID, wsURL, nil)
	if errSecond != nil {
		t.Fatal(errSecond)
	}
	if first == second || firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("proxy switch reused connection: first=%d second=%d", firstHits.Load(), secondHits.Load())
	}
	cfg.ProxyURL = "invalid-global-proxy"
	reused, _, _, errReuse := exec.ensureUpstreamConn(context.Background(), auth, sess, auth.ID, wsURL, nil)
	if errReuse != nil || reused != second || secondHits.Load() != 1 {
		t.Fatalf("unchanged proxy should reuse connection: %v", errReuse)
	}
	auth.ProxyURL = "invalid-proxy"
	if _, _, _, errInvalid := exec.ensureUpstreamConn(context.Background(), auth, sess, auth.ID, wsURL, nil); errInvalid == nil {
		t.Fatal("invalid changed proxy reused a connection or fell back")
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatal("invalid proxy fell back to another proxy")
	}
}

func TestCodexRequiredWebsocketRejectsChangedProxy(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			cfg := &config.Config{}
			exec := NewCodexWebsocketsExecutor(cfg)
			exec.store = &codexWebsocketSessionStore{}
			auth := &cliproxyauth.Auth{ID: "credential", Provider: "codex", ProxyURL: "direct", Metadata: map[string]any{"access_token": "synthetic"}}
			sess := exec.getOrCreateSession(t.Name())
			conn := &websocket.Conn{}
			sess.conn, sess.connCloser = conn, newWebsocketConnectionCloser(conn)
			sess.authID, sess.wsURL = auth.ID, "wss://chatgpt.com/backend-api/codex/responses"
			sess.connectionKey = helps.CodexWebsocketConnectionKey(cfg, auth)
			sess.resetUpstreamDisconnectError(conn)
			auth.ProxyURL = "http://127.0.0.1:1"
			ctx := cliproxyexecutor.WithRequiredUpstreamWebsocket(cliproxyexecutor.WithDownstreamWebsocket(context.Background()))
			req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":[]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: translator.FormatOpenAIResponse, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()}}
			var err error
			if stream {
				_, err = exec.ExecuteStream(ctx, auth, req, opts)
			} else {
				_, err = exec.Execute(ctx, auth, req, opts)
			}
			if !cliproxyexecutor.IsUpstreamWebsocketReplayRequired(err) {
				t.Fatalf("changed proxy reused the old connection: %v", err)
			}
		})
	}
}
