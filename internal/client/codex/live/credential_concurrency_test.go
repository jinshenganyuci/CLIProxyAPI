package live

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type concurrencyLiveExecutor struct {
	captureExecutor
	calls atomic.Int32
	serve func(context.Context, *auth.Auth, *http.Request, int32) (*http.Response, error)
}

func (e *concurrencyLiveExecutor) HttpRequest(ctx context.Context, selected *auth.Auth, request *http.Request) (*http.Response, error) {
	n := e.calls.Add(1)
	if e.serve != nil {
		return e.serve(ctx, selected, request, n)
	}
	return &http.Response{
		StatusCode: http.StatusCreated,
		Header: http.Header{
			"Content-Type": {"application/sdp"},
			"Location":     {fmt.Sprintf("/v1/live/call-%d", n)},
		},
		Body: io.NopCloser(strings.NewReader("v=0\r\n")),
	}, nil
}

func newConcurrencyLiveHandler(t *testing.T, limit int) (*Handler, *auth.Manager, *concurrencyLiveExecutor) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	executor := &concurrencyLiveExecutor{}
	manager.RegisterExecutor(executor)
	registerCredential(t, manager, &auth.Auth{
		ID: "limited-live", Provider: "codex", Status: auth.StatusActive,
		Metadata: map[string]any{"access_token": "synthetic-live-token", "concurrency_limit": limit},
	})
	handler := NewHandler(manager, nil)
	t.Cleanup(handler.Close)
	return handler, manager, executor
}

func serveConcurrencyLive(router http.Handler) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(`{"sdp":"v=0\r\n","session":{"model":"gpt-live-1-codex"}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func requireLiveInflight(t *testing.T, manager *auth.Manager, expected int) {
	t.Helper()
	if got, _ := manager.CredentialConcurrencySnapshot("limited-live"); got != expected {
		t.Fatalf("credential inflight = %d, want %d", got, expected)
	}
}

func TestCredentialConcurrencyLiveRetainsUntilHangupAndClose(t *testing.T) {
	handler, manager, executor := newConcurrencyLiveHandler(t, 1)
	router := gin.New()
	router.POST("/v1/live", handler.Handle)
	router.POST("/v1/realtime/calls/:call_id/hangup", handler.HandleHangup)
	first := serveConcurrencyLive(router)
	if first.Code != http.StatusCreated {
		t.Fatalf("first call status = %d, body %s", first.Code, first.Body.String())
	}
	requireLiveInflight(t, manager, 1)
	blocked := serveConcurrencyLive(router)
	if blocked.Code != http.StatusTooManyRequests || executor.calls.Load() != 1 {
		t.Fatalf("overflow status/calls = %d/%d, want 429/1; body %s", blocked.Code, executor.calls.Load(), blocked.Body.String())
	}
	hangup := httptest.NewRecorder()
	router.ServeHTTP(hangup, httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-1/hangup", nil))
	if hangup.Code != http.StatusCreated {
		t.Fatalf("hangup while full status = %d, body %s", hangup.Code, hangup.Body.String())
	}
	requireLiveInflight(t, manager, 0)
	if next := serveConcurrencyLive(router); next.Code != http.StatusCreated {
		t.Fatalf("call after hangup status = %d, body %s", next.Code, next.Body.String())
	}
	requireLiveInflight(t, manager, 1)
	handler.Close()
	requireLiveInflight(t, manager, 0)
}

func TestCredentialConcurrencyLiveFailuresRelease(t *testing.T) {
	for _, scenario := range []string{"upstream_error", "transport_error", "read_error", "missing_call_id"} {
		t.Run(scenario, func(t *testing.T) {
			handler, manager, executor := newConcurrencyLiveHandler(t, 1)
			executor.serve = func(_ context.Context, _ *auth.Auth, _ *http.Request, _ int32) (*http.Response, error) {
				requireLiveInflight(t, manager, 1)
				if scenario == "transport_error" {
					return nil, errors.New("synthetic transport failure")
				}
				response := &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": {"application/sdp"}}, Body: io.NopCloser(strings.NewReader("v=0\r\n"))}
				if scenario != "missing_call_id" {
					response.Header.Set("Location", "/v1/live/failed-call")
				}
				if scenario == "upstream_error" {
					response.StatusCode = http.StatusBadGateway
				}
				if scenario == "read_error" {
					response.Body = &errorResponseBody{payload: []byte("partial")}
				}
				return response, nil
			}
			router := gin.New()
			router.POST("/v1/live", handler.Handle)
			serveConcurrencyLive(router)
			if executor.calls.Load() != 1 {
				t.Fatalf("bootstrap did not reach upstream: %d calls", executor.calls.Load())
			}
			requireLiveInflight(t, manager, 0)
		})
	}
}

type concurrencyFailingResponseWriter struct {
	*httptest.ResponseRecorder
}

func (w concurrencyFailingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic downstream write failure")
}

func TestCredentialConcurrencyLiveFailedResponseWriteReleases(t *testing.T) {
	handler, manager, executor := newConcurrencyLiveHandler(t, 1)
	router := gin.New()
	router.POST("/v1/live", handler.Handle)
	request := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(`{"sdp":"v=0\r\n"}`))
	request.Header.Set("Content-Type", "application/json")
	writer := concurrencyFailingResponseWriter{httptest.NewRecorder()}
	router.ServeHTTP(writer, request)
	if executor.calls.Load() != 1 {
		t.Fatalf("bootstrap did not reach upstream: %d calls", executor.calls.Load())
	}
	if _, exists := handler.sessions.peek("call-1"); exists {
		t.Fatal("failed downstream response retained the call")
	}
	requireLiveInflight(t, manager, 0)
}

func TestCredentialConcurrencyLiveCanceledBootstrapReleases(t *testing.T) {
	handler, manager, executor := newConcurrencyLiveHandler(t, 1)
	entered := make(chan struct{})
	executor.serve = func(ctx context.Context, _ *auth.Auth, _ *http.Request, _ int32) (*http.Response, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(`{"sdp":"v=0\r\n"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	router := gin.New()
	router.POST("/v1/live", handler.Handle)
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(httptest.NewRecorder(), request.WithContext(ctx))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not reach upstream")
	}
	requireLiveInflight(t, manager, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not stop after cancellation")
	}
	requireLiveInflight(t, manager, 0)
}

func TestCredentialConcurrencyLiveSessionResourceLifecycles(t *testing.T) {
	for _, scenario := range []string{"replace", "expire", "claimed_expire", "complete", "close_all"} {
		t.Run(scenario, func(t *testing.T) {
			handler, manager, _ := newConcurrencyLiveHandler(t, 3)
			selected, _ := manager.GetByID("limited-live")
			reserve := func() liveSession {
				release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), selected)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				return liveSession{authID: selected.ID, releaseConcurrency: release}
			}
			stored := handler.sessions.put("lifecycle", reserve())
			requireLiveInflight(t, manager, 1)
			switch scenario {
			case "replace":
				replacement := handler.sessions.put("lifecycle", reserve())
				requireLiveInflight(t, manager, 1)
				handler.sessions.complete(stored, "stale_close")
				requireLiveInflight(t, manager, 1)
				handler.sessions.complete(replacement, "closed")
			case "expire":
				handler.sessions.expire(stored.callID, stored.token)
			case "claimed_expire":
				claimed, status := handler.sessions.claim(stored.callID)
				if status != sessionClaimAcquired {
					t.Fatal("session was not claimable")
				}
				handler.sessions.expire(stored.callID, stored.token)
				requireLiveInflight(t, manager, 1)
				handler.sessions.release(claimed)
				handler.sessions.expire(stored.callID, stored.token)
			case "complete":
				handler.sessions.complete(stored, "closed")
				handler.sessions.complete(stored, "closed_again")
			case "close_all":
				handler.Close()
			}
			requireLiveInflight(t, manager, 0)
		})
	}
}

type concurrencyClosingMediaSession struct {
	fakeMediaSession
	beforeClose func()
}

func (s *concurrencyClosingMediaSession) CloseWithReason(reason string) error {
	s.beforeClose()
	return s.fakeMediaSession.CloseWithReason(reason)
}

func TestCredentialConcurrencyLiveReleasesAfterResourcesAndMediaClose(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace_%t", replace), func(t *testing.T) {
			handler, manager, _ := newConcurrencyLiveHandler(t, 3)
			selected, _ := manager.GetByID("limited-live")
			release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), selected)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer release()
			expectedDuringClose := 1
			if replace {
				expectedDuringClose = 2
			}
			resources := &liveSessionResources{}
			resources.add(func() error {
				requireLiveInflight(t, manager, expectedDuringClose)
				return nil
			})
			media := &concurrencyClosingMediaSession{beforeClose: func() {
				requireLiveInflight(t, manager, expectedDuringClose)
			}}
			stored := handler.sessions.put("closing-order", liveSession{authID: selected.ID, resources: resources, media: media, releaseConcurrency: release})
			if replace {
				releaseReplacement, errReplacement := manager.AcquireCredentialConcurrency(context.Background(), selected)
				if errReplacement != nil {
					t.Fatal(errReplacement)
				}
				defer releaseReplacement()
				replacement := handler.sessions.put("closing-order", liveSession{authID: selected.ID, releaseConcurrency: releaseReplacement})
				requireLiveInflight(t, manager, 1)
				handler.sessions.complete(replacement, "closed")
			} else {
				handler.sessions.complete(stored, "closed")
			}
			if !media.closed.Load() {
				t.Fatal("session did not close its media")
			}
			requireLiveInflight(t, manager, 0)
		})
	}
}

func TestCredentialConcurrencyLiveCloseRejectsLateBootstrapRetention(t *testing.T) {
	handler, manager, executor := newConcurrencyLiveHandler(t, 1)
	entered := make(chan struct{})
	finish := make(chan struct{})
	executor.serve = func(ctx context.Context, _ *auth.Auth, _ *http.Request, _ int32) (*http.Response, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-finish:
			return &http.Response{
				StatusCode: http.StatusCreated,
				Header:     http.Header{"Content-Type": {"application/sdp"}, "Location": {"/v1/live/late-call"}},
				Body:       io.NopCloser(strings.NewReader("v=0\r\n")),
			}, nil
		}
	}
	router := gin.New()
	router.POST("/v1/live", handler.Handle)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serveConcurrencyLive(router) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not reach upstream")
	}
	handler.Close()
	requireLiveInflight(t, manager, 1)
	close(finish)
	select {
	case response := <-done:
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("late bootstrap status = %d, body %s", response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not finish")
	}
	if _, exists := handler.sessions.peek("late-call"); exists {
		t.Fatal("bootstrap retained a session after handler close")
	}
	requireLiveInflight(t, manager, 0)
}

func TestCredentialConcurrencyDirectWebsocketLifetime(t *testing.T) {
	for _, closeMode := range []string{"client_close", "context_cancel", "handler_close"} {
		t.Run(closeMode, func(t *testing.T) {
			handler, manager, _ := newConcurrencyLiveHandler(t, 1)
			upstreamCalls := atomic.Int32{}
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				upstreamCalls.Add(1)
				connection, errUpgrade := sidebandUpgrader.Upgrade(writer, request, nil)
				if errUpgrade != nil {
					return
				}
				defer func() { _ = connection.Close() }()
				for {
					if _, _, errRead := connection.ReadMessage(); errRead != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/v1"
			cancels := make(chan context.CancelFunc, 3)
			finished := make(chan struct{}, 3)
			router := gin.New()
			router.GET("/v1/realtime", func(c *gin.Context) {
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				cancels <- cancel
				handler.HandleRealtimeWebsocket(c)
				finished <- struct{}{}
			})
			downstream := httptest.NewServer(router)
			defer downstream.Close()
			url := "ws" + strings.TrimPrefix(downstream.URL, "http") + "/v1/realtime"
			connection, _, errDial := websocket.DefaultDialer.Dial(url, nil)
			if errDial != nil {
				t.Fatalf("first websocket: %v", errDial)
			}
			defer func() { _ = connection.Close() }()
			cancelActive := <-cancels
			requireLiveInflight(t, manager, 1)
			overflow, response, errOverflow := websocket.DefaultDialer.Dial(url, nil)
			if overflow != nil {
				_ = overflow.Close()
			}
			if response != nil && response.Body != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if errOverflow == nil || response == nil || response.StatusCode != http.StatusTooManyRequests || upstreamCalls.Load() != 1 {
				t.Fatalf("overflow error/response/upstream = %v/%v/%d", errOverflow, response, upstreamCalls.Load())
			}
			<-finished
			if closeMode == "context_cancel" {
				cancelActive()
			} else if closeMode == "handler_close" {
				handler.Close()
			} else {
				_ = connection.Close()
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("websocket handler did not finish")
			}
			requireLiveInflight(t, manager, 0)
		})
	}
}

func TestCredentialConcurrencyDirectWebsocketRejectedHandshakeReleases(t *testing.T) {
	handler, manager, _ := newConcurrencyLiveHandler(t, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/v1"
	router := gin.New()
	router.GET("/v1/realtime", handler.HandleRealtimeWebsocket)
	request := httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("handshake status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	requireLiveInflight(t, manager, 0)
}

func TestCredentialConcurrencySidebandReusesFullLiveSession(t *testing.T) {
	handler, manager, _ := newConcurrencyLiveHandler(t, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, errUpgrade := sidebandUpgrader.Upgrade(writer, request, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		for {
			messageType, message, errRead := connection.ReadMessage()
			if errRead != nil {
				return
			}
			if errWrite := connection.WriteMessage(messageType, message); errWrite != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/v1"
	finished := make(chan struct{}, 1)
	router := gin.New()
	router.POST("/v1/live", handler.Handle)
	router.GET("/v1/realtime", func(c *gin.Context) {
		handler.HandleRealtimeWebsocket(c)
		finished <- struct{}{}
	})
	if response := serveConcurrencyLive(router); response.Code != http.StatusCreated {
		t.Fatalf("bootstrap status = %d, body %s", response.Code, response.Body.String())
	}
	requireLiveInflight(t, manager, 1)
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	url := "ws" + strings.TrimPrefix(downstream.URL, "http") + "/v1/realtime?call_id=call-1"
	connection, _, errDial := websocket.DefaultDialer.Dial(url, nil)
	if errDial != nil {
		t.Fatalf("sideband must reuse the occupied slot: %v", errDial)
	}
	defer func() { _ = connection.Close() }()
	if errWrite := connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.update"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, _, errRead := connection.ReadMessage(); errRead != nil {
		t.Fatal(errRead)
	}
	requireLiveInflight(t, manager, 1)
	_ = connection.Close()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("sideband did not finish after client close")
	}
	requireLiveInflight(t, manager, 0)
}
