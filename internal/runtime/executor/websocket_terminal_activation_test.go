package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWebsocketTerminalReadBeforeActivation(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		for _, terminal := range []string{"eof", "binary"} {
			t.Run(provider+"/"+terminal, func(t *testing.T) {
				upgrader := websocket.Upgrader{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, errUpgrade := upgrader.Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Errorf("upgrade websocket: %v", errUpgrade)
						return
					}
					defer func() { _ = conn.Close() }()
					if terminal == "binary" {
						if errWrite := conn.WriteMessage(websocket.BinaryMessage, []byte("unexpected")); errWrite != nil {
							t.Errorf("write binary frame: %v", errWrite)
						}
					}
				}))
				defer server.Close()
				conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
				if errDial != nil {
					t.Fatal(errDial)
				}
				defer func() { _ = conn.Close() }()
				sess := &codexWebsocketSession{conn: conn, connCloser: newWebsocketConnectionCloser(conn)}
				sess.resetUpstreamDisconnectError(conn)
				// Complete the reader before installing the request's channel.
				if provider == "codex" {
					(&CodexWebsocketsExecutor{}).readUpstreamLoop(sess, conn)
				} else {
					(&XAIWebsocketsExecutor{}).readUpstreamLoop(sess, conn)
				}
				terminalErr := sess.upstreamDisconnectError(conn)
				if terminalErr == nil {
					t.Fatal("reader discarded the terminal error before activation")
				}
				ch := sess.activate(conn)
				defer sess.clearActive(conn, ch)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				var errRead error
				if provider == "codex" {
					_, _, errRead = readCodexWebsocketMessage(ctx, sess, conn, ch)
				} else {
					_, _, errRead = readXAIWebsocketMessage(ctx, sess, conn, ch)
				}
				if !errors.Is(errRead, terminalErr) {
					t.Fatalf("late activation read = %v, want retained terminal error %v", errRead, terminalErr)
				}
			})
		}
	}
}

func TestWebsocketTerminalReadPreservesBufferedEventsAndConnectionIdentity(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		t.Run(provider, func(t *testing.T) {
			oldConn, conn := &websocket.Conn{}, &websocket.Conn{}
			sess := &codexWebsocketSession{}
			sess.resetUpstreamDisconnectError(oldConn)
			sess.setUpstreamDisconnectError(oldConn, errors.New("old connection closed"))
			sess.resetUpstreamDisconnectError(conn)
			sess.setUpstreamDisconnectError(oldConn, errors.New("late old connection close"))
			if got := sess.upstreamDisconnectError(conn); got != nil {
				t.Fatalf("old connection contaminated current terminal state: %v", got)
			}
			ch := sess.activate(conn)
			defer sess.clearActive(conn, ch)
			payload := []byte(`{"type":"response.completed","response":{"output":[]}}`)
			ch <- codexWebsocketRead{conn: conn, msgType: websocket.TextMessage, payload: payload}
			sess.setUpstreamDisconnectError(conn, io.ErrUnexpectedEOF)
			read := readCodexWebsocketMessage
			if provider == "xai" {
				read = readXAIWebsocketMessage
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			msgType, got, errRead := read(ctx, sess, conn, ch)
			if errRead != nil || msgType != websocket.TextMessage || string(got) != string(payload) {
				t.Fatalf("buffered completion was overtaken by terminal state: type=%d payload=%s error=%v", msgType, got, errRead)
			}
			if _, _, errRead = read(ctx, sess, conn, ch); !errors.Is(errRead, io.ErrUnexpectedEOF) {
				t.Fatalf("drained read = %v, want connection's terminal error", errRead)
			}
			close(ch)
			if _, _, errRead = read(ctx, sess, conn, ch); !errors.Is(errRead, io.ErrUnexpectedEOF) {
				t.Fatalf("closed channel read = %v, want connection's terminal error", errRead)
			}
		})
	}
}
