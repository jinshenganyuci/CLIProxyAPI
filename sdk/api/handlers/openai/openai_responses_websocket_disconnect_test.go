package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

type websocketCancellationCall struct {
	ctx     context.Context
	payload []byte
	finish  chan struct{}
}

type websocketCancellationExecutor struct {
	websocketUpstreamDisconnectExecutor
	bootstrap   bool
	emitStarted bool
	started     chan websocketCancellationCall
	canceled    chan error
	closed      chan string
	cleanup     chan struct{}
	active      atomic.Int32
	peak        atomic.Int32
	calls       atomic.Int32
}

func (e *websocketCancellationExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	call := e.calls.Add(1)
	active := e.active.Add(1)
	for previous := e.peak.Load(); active > previous && !e.peak.CompareAndSwap(previous, active); previous = e.peak.Load() {
	}
	finish := make(chan struct{})
	e.started <- websocketCancellationCall{ctx: ctx, payload: append([]byte(nil), req.Payload...), finish: finish}
	if e.bootstrap {
		defer e.active.Add(-1)
		select {
		case <-ctx.Done():
			e.canceled <- ctx.Err()
			return nil, ctx.Err()
		case <-e.cleanup:
			return nil, context.Canceled
		}
	}
	chunks := make(chan coreexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		defer e.active.Add(-1)
		if e.emitStarted {
			select {
			case chunks <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created","response":{"id":"response-started","output":[]}}` + "\n\n")}:
			case <-ctx.Done():
				e.canceled <- ctx.Err()
				return
			case <-e.cleanup:
				return
			}
		}
		select {
		case <-ctx.Done():
			e.canceled <- ctx.Err()
		case <-e.cleanup:
		case <-finish:
			payload := []byte(fmt.Sprintf(`data: {"type":"response.completed","response":{"id":"response-%d","output":[]}}`+"\n\n", call))
			select {
			case chunks <- coreexecutor.StreamChunk{Payload: payload}:
			case <-ctx.Done():
			case <-e.cleanup:
			}
		}
	}()
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketCancellationExecutor) CloseExecutionSession(sessionID string) {
	select {
	case e.closed <- sessionID:
	default:
	}
}

func newWebsocketCancellationTest(t *testing.T, bootstrap bool, emitStarted ...bool) (*websocket.Conn, *websocketCancellationExecutor, <-chan struct{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	executor := &websocketCancellationExecutor{
		bootstrap: bootstrap,
		started:   make(chan websocketCancellationCall, 8),
		canceled:  make(chan error, 8),
		closed:    make(chan string, 1),
		cleanup:   make(chan struct{}),
	}
	if len(emitStarted) > 0 {
		executor.emitStarted = emitStarted[0]
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: t.Name(), Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	done := make(chan struct{})
	router := gin.New()
	router.GET("/v1/responses/ws", func(c *gin.Context) {
		defer close(done)
		h.ResponsesWebsocket(c)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(executor.cleanup) })
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, executor, done
}

func awaitWebsocketCancellationCall(t *testing.T, executor *websocketCancellationExecutor) websocketCancellationCall {
	t.Helper()
	select {
	case call := <-executor.started:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ExecuteStream")
		return websocketCancellationCall{}
	}
}

func TestResponsesWebsocketDownstreamDisconnectCancelsSilentExecution(t *testing.T) {
	for _, stage := range []string{"executor-bootstrap", "stream-bootstrap", "stream-started"} {
		for _, queued := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/queued=%t", stage, queued), func(t *testing.T) {
				conn, executor, done := newWebsocketCancellationTest(t, stage == "executor-bootstrap", stage == "stream-started")
				request := []byte(`{"type":"response.create","model":"test-model","input":[]}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, request); errWrite != nil {
					t.Fatalf("send request: %v", errWrite)
				}
				call := awaitWebsocketCancellationCall(t, executor)
				if stage == "stream-started" {
					if errDeadline := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); errDeadline != nil {
						t.Fatalf("set test read deadline: %v", errDeadline)
					}
					if _, payload, errRead := conn.ReadMessage(); errRead != nil || gjson.GetBytes(payload, "type").String() != "response.created" {
						t.Fatalf("upstream stream did not start: payload = %s, error = %v", payload, errRead)
					}
				}
				if queued {
					if errWrite := conn.WriteMessage(websocket.TextMessage, request); errWrite != nil {
						t.Fatalf("send queued request: %v", errWrite)
					}
				}
				if errClose := conn.Close(); errClose != nil {
					t.Fatalf("close downstream: %v", errClose)
				}
				select {
				case errCanceled := <-executor.canceled:
					if !errors.Is(errCanceled, context.Canceled) || !errors.Is(call.ctx.Err(), context.Canceled) {
						t.Fatalf("upstream cancellation = %v, context = %v", errCanceled, call.ctx.Err())
					}
				case <-time.After(3 * time.Second):
					t.Fatal("silent upstream execution was not canceled when downstream disconnected")
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("WebSocket handler did not exit after downstream disconnect")
				}
				select {
				case sessionID := <-executor.closed:
					if sessionID == "" {
						t.Fatal("upstream execution session close did not include session ID")
					}
				default:
					t.Fatal("upstream execution session was not closed")
				}
				if got := executor.calls.Load(); got != 1 {
					t.Fatalf("started %d requests, want only the active turn", got)
				}
			})
		}
	}
}

func TestResponsesWebsocketQueuedTurnRemainsSequential(t *testing.T) {
	conn, executor, _ := newWebsocketCancellationTest(t, false)
	pong := make(chan struct{}, 1)
	conn.SetPongHandler(func(string) error {
		pong <- struct{}{}
		return nil
	})
	type readResult struct {
		payload []byte
		err     error
	}
	responses := make(chan readResult, 2)
	go func() {
		for range 2 {
			_, payload, errRead := conn.ReadMessage()
			responses <- readResult{payload: payload, err: errRead}
			if errRead != nil {
				return
			}
		}
	}()
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","input":[]}`)); errWrite != nil {
		t.Fatalf("send first request: %v", errWrite)
	}
	first := awaitWebsocketCancellationCall(t, executor)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","previous_response_id":"response-1","input":[]}`)); errWrite != nil {
		t.Fatalf("send next request: %v", errWrite)
	}
	if errPing := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(3*time.Second)); errPing != nil {
		t.Fatalf("send ping after queued request: %v", errPing)
	}
	select {
	case <-pong:
	case <-time.After(3 * time.Second):
		t.Fatal("reader stopped before processing ping behind queued request")
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("started %d requests while the first turn was still executing", got)
	}
	if errContext := first.ctx.Err(); errContext != nil {
		t.Fatalf("queued next request canceled current turn: %v", errContext)
	}
	close(first.finish)
	select {
	case result := <-responses:
		if result.err != nil || gjson.GetBytes(result.payload, "response.id").String() != "response-1" {
			t.Fatalf("first response = %s, error = %v", result.payload, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first response")
	}
	second := awaitWebsocketCancellationCall(t, executor)
	if got := gjson.GetBytes(second.payload, "previous_response_id").String(); got != "response-1" {
		t.Fatalf("queued continuation previous_response_id = %q", got)
	}
	close(second.finish)
	select {
	case result := <-responses:
		if result.err != nil || gjson.GetBytes(result.payload, "response.id").String() != "response-2" {
			t.Fatalf("second response = %s, error = %v", result.payload, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second response")
	}
	if got := executor.peak.Load(); got != 1 {
		t.Fatalf("peak concurrent executions = %d, want 1", got)
	}
}

func TestResponsesWebsocketPendingRequestOverflowCancelsExecution(t *testing.T) {
	conn, executor, done := newWebsocketCancellationTest(t, false)
	request := []byte(`{"type":"response.create","model":"test-model","input":[]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, request); errWrite != nil {
		t.Fatalf("send request: %v", errWrite)
	}
	awaitWebsocketCancellationCall(t, executor)
	for range wsPendingRequestLimit + 1 {
		if errWrite := conn.WriteMessage(websocket.TextMessage, request); errWrite != nil {
			t.Fatalf("send queued request: %v", errWrite)
		}
	}
	select {
	case <-executor.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("too many queued requests did not cancel active upstream execution")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("WebSocket handler did not exit after pending request overflow")
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("executed %d requests, want only the original active turn", got)
	}
}

func TestResponsesWebsocketCloseFrameCancelsExecution(t *testing.T) {
	conn, executor, done := newWebsocketCancellationTest(t, false)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","input":[]}`)); errWrite != nil {
		t.Fatalf("send request: %v", errWrite)
	}
	awaitWebsocketCancellationCall(t, executor)
	if errWrite := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "finished"), time.Now().Add(3*time.Second)); errWrite != nil {
		t.Fatalf("send close frame: %v", errWrite)
	}
	select {
	case <-executor.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("WebSocket close frame did not cancel active upstream execution")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("WebSocket handler did not exit after close frame")
	}
}
