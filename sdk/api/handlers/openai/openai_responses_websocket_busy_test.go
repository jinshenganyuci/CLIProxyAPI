package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

type websocketBusyExecutor struct {
	calls    atomic.Int32
	started  chan context.Context
	canceled chan error
	cleanup  chan struct{}
}

func (e *websocketBusyExecutor) Identifier() string { return "codex" }

func (e *websocketBusyExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected non-stream execution")
}

func (e *websocketBusyExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	e.started <- ctx
	chunks := make(chan coreexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		select {
		case chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"busy-response\",\"output\":[]}}\n\n")}:
		case <-ctx.Done():
			e.canceled <- ctx.Err()
			return
		case <-e.cleanup:
			return
		}
		select {
		case <-ctx.Done():
			e.canceled <- ctx.Err()
		case <-e.cleanup:
		}
	}()
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketBusyExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketBusyExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected token count")
}

func (e *websocketBusyExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HTTP request")
}

func readWebsocketBusyPayload(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	if errDeadline := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); errDeadline != nil {
		t.Fatalf("set test read deadline: %v", errDeadline)
	}
	kind, payload, errRead := conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read websocket JSON payload: %v", errRead)
	}
	if kind != websocket.TextMessage || !gjson.ValidBytes(payload) {
		t.Fatalf("message type = %d, payload = %s; want JSON text", kind, payload)
	}
	return payload
}

func assertWebsocketCredentialBusyPayload(t *testing.T, payload []byte) {
	t.Helper()
	if gjson.GetBytes(payload, "type").String() != "error" || gjson.GetBytes(payload, "status").Int() != http.StatusTooManyRequests || gjson.GetBytes(payload, "error.code").String() != "credential_concurrency_exceeded" {
		t.Fatalf("want credential concurrency error with status 429, got %s", payload)
	}
}

func TestResponsesWebsocketCredentialConcurrencyRejectsFourthActiveTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &websocketBusyExecutor{
		started: make(chan context.Context, 4), canceled: make(chan error, 4), cleanup: make(chan struct{}),
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID: t.Name(), Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"}, Metadata: map[string]any{"concurrency_limit": 3},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register credential: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	done := make(chan struct{}, 4)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		defer func() { done <- struct{}{} }()
		handler.ResponsesWebsocket(c)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(executor.cleanup) })
	connections := make([]*websocket.Conn, 4)
	upstreamContexts := make([]context.Context, 0, 3)
	for i := range connections {
		conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
		if errDial != nil {
			t.Fatalf("dial websocket %d: %v", i+1, errDial)
		}
		connections[i] = conn
		t.Cleanup(func() { _ = conn.Close() })
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","input":[]}`)); errWrite != nil {
			t.Fatalf("send websocket request %d: %v", i+1, errWrite)
		}
		payload := readWebsocketBusyPayload(t, conn)
		if i == 3 {
			assertWebsocketCredentialBusyPayload(t, payload)
			continue
		}
		if event := gjson.GetBytes(payload, "type").String(); event != "response.created" {
			t.Fatalf("request %d did not start: %s", i+1, payload)
		}
		select {
		case ctx := <-executor.started:
			upstreamContexts = append(upstreamContexts, ctx)
		case <-time.After(3 * time.Second):
			t.Fatal("upstream execution was not recorded")
		}
	}
	if calls := executor.calls.Load(); calls != 3 {
		t.Fatalf("executor received %d calls, want only 3 admitted requests", calls)
	}
	if inflight, limit := manager.CredentialConcurrencySnapshot(auth.ID); inflight != 3 || limit != 3 {
		t.Fatalf("credential concurrency = %d/%d, want 3/3", inflight, limit)
	}
	for _, conn := range connections[:3] {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close active downstream: %v", errClose)
		}
	}
	for range 3 {
		select {
		case errCanceled := <-executor.canceled:
			if !errors.Is(errCanceled, context.Canceled) {
				t.Fatalf("upstream error = %v, want context cancellation", errCanceled)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("closing downstream did not cancel silent upstream")
		}
	}
	for range 4 {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("websocket handler did not exit")
		}
	}
	for _, ctx := range upstreamContexts {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("upstream context = %v, want canceled", ctx.Err())
		}
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	check := time.NewTicker(time.Millisecond)
	defer check.Stop()
	for {
		inflight, limit := manager.CredentialConcurrencySnapshot(auth.ID)
		if inflight == 0 && limit == 3 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("credential concurrency after disconnect = %d/%d, want 0/3", inflight, limit)
		case <-check.C:
		}
	}
}

func TestForwardResponsesWebsocketClosedDataPreservesBufferedCredentialBusy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	serverResults := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			serverResults <- errUpgrade
			return
		}
		defer func() { _ = conn.Close() }()
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		data := make(chan []byte)
		close(data)
		errs := make(chan *interfaces.ErrorMessage, 1)
		busy := &coreauth.Error{Code: "credential_concurrency_exceeded", Message: "credential busy", HTTPStatus: http.StatusTooManyRequests}
		errs <- handlers.ExecutionErrorMessage(busy)
		close(errs)
		handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
		_, _, _, errMessage, errForward := handler.forwardResponsesWebsocket(ctx, newResponsesWebsocketWriter(conn), func(...interface{}) {}, data, errs, nil, "busy-forward")
		if errForward != nil && !errors.Is(errForward, websocket.ErrCloseSent) {
			serverResults <- errForward
			return
		}
		if errMessage == nil || errMessage.StatusCode != http.StatusTooManyRequests || !coreauth.IsCredentialConcurrencyError(errMessage.Error) {
			serverResults <- fmt.Errorf("buffered local busy error was lost: %#v", errMessage)
			return
		}
		serverResults <- nil
	}))
	t.Cleanup(server.Close)
	// Both channels are ready before selection; repeated handshakes cover either select branch.
	for attempt := range 64 {
		t.Run(fmt.Sprintf("attempt-%d", attempt), func(t *testing.T) {
			conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if errDial != nil {
				t.Fatalf("dial websocket: %v", errDial)
			}
			t.Cleanup(func() { _ = conn.Close() })
			t.Cleanup(func() {
				select {
				case errServer := <-serverResults:
					if errServer != nil {
						t.Error(errServer)
					}
				case <-time.After(3 * time.Second):
					t.Error("forwarding did not finish")
				}
			})
			assertWebsocketCredentialBusyPayload(t, readWebsocketBusyPayload(t, conn))
		})
	}
}

func TestResponsesWebsocketExposesOnlyTypedCredentialBusy429(t *testing.T) {
	busy := &coreauth.Error{Code: "credential_concurrency_exceeded", Message: "credential busy", HTTPStatus: http.StatusTooManyRequests}
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "local admission", err: busy, want: true},
		{name: "wrapped local admission", err: fmt.Errorf("execute: %w", busy), want: true},
		{name: "upstream quota", err: errors.New(`{"error":{"code":"usage_limit_reached","message":"quota exhausted"}}`)},
		{name: "upstream lookalike", err: errors.New(`{"error":{"code":"credential_concurrency_exceeded","message":"upstream busy"}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: test.err}
			if got := shouldExposeResponsesUpstreamError(message); got != test.want {
				t.Fatalf("error exposure = %t, want %t for %v", got, test.want, test.err)
			}
			if got := shouldReplayResponsesWebsocketPinnedAuthFailure(message); got == test.want {
				t.Fatalf("pinned auth replay = %t, want %t for %v", got, !test.want, test.err)
			}
		})
	}
}
