package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type alphaConcurrencyExecutor struct {
	codexSearchCaptureExecutor
	calls atomic.Int32
	serve func(context.Context, *auth.Auth, *http.Request) (*http.Response, error)
}

func (e *alphaConcurrencyExecutor) HttpRequest(ctx context.Context, selected *auth.Auth, request *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	return e.serve(ctx, selected, request)
}

type alphaConcurrencyBody struct {
	ctx     context.Context
	entered chan struct{}
	finish  <-chan struct{}
	once    sync.Once
	reader  io.Reader
	err     error
	closed  atomic.Bool
}

func (b *alphaConcurrencyBody) Read(payload []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-b.finish:
		if b.err != nil {
			return 0, b.err
		}
		return b.reader.Read(payload)
	}
}

func (b *alphaConcurrencyBody) Close() error {
	b.closed.Store(true)
	return nil
}

func alphaConcurrencyRequest(ctx context.Context) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"synthetic concurrency test"}`)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestCodexAlphaSearchConcurrencyHeldUntilBodyCompletes(t *testing.T) {
	for _, scenario := range []string{"complete", "read_error", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			server := newTestServer(t)
			manager := server.handlers.AuthManager
			credential := &auth.Auth{
				ID: "alpha-limited", Provider: "codex", Status: auth.StatusActive,
				Metadata: map[string]any{"access_token": "synthetic-alpha-token", "concurrency_limit": 1},
			}
			if _, errRegister := manager.Register(context.Background(), credential); errRegister != nil {
				t.Fatal(errRegister)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finish := make(chan struct{})
			body := &alphaConcurrencyBody{ctx: ctx, entered: make(chan struct{}), finish: finish, reader: strings.NewReader(`{"results":[]}`)}
			if scenario == "read_error" {
				body.err = errors.New("synthetic read failure")
			}
			executor := &alphaConcurrencyExecutor{serve: func(_ context.Context, _ *auth.Auth, request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/quota" {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
			}}
			manager.RegisterExecutor(executor)
			recorder := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				server.engine.ServeHTTP(recorder, alphaConcurrencyRequest(ctx))
			}()
			select {
			case <-body.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("search did not start reading upstream body")
			}
			if inflight, _ := manager.CredentialConcurrencySnapshot(credential.ID); inflight != 1 {
				t.Fatalf("reading body inflight = %d, want 1", inflight)
			}
			blocked := httptest.NewRecorder()
			server.engine.ServeHTTP(blocked, alphaConcurrencyRequest(context.Background()))
			if blocked.Code != http.StatusTooManyRequests || executor.calls.Load() != 1 {
				t.Fatalf("overflow status/calls = %d/%d, body %s", blocked.Code, executor.calls.Load(), blocked.Body.String())
			}

			// Direct management/quota probes deliberately share transport without consuming inference capacity.
			quota, errQuota := manager.HttpRequest(context.Background(), credential, httptest.NewRequest(http.MethodGet, "http://synthetic.local/quota", nil))
			if errQuota != nil {
				t.Fatalf("management request while credential full: %v", errQuota)
			}
			if errClose := quota.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if inflight, _ := manager.CredentialConcurrencySnapshot(credential.ID); inflight != 1 {
				t.Fatalf("management request changed inflight to %d", inflight)
			}
			if scenario == "cancel" {
				cancel()
			} else {
				close(finish)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("search did not complete after releasing response body")
			}
			if inflight, _ := manager.CredentialConcurrencySnapshot(credential.ID); inflight != 0 {
				t.Fatalf("finished request inflight = %d, want 0", inflight)
			}
			if !body.closed.Load() {
				t.Fatal("upstream response body was not closed")
			}
			if scenario == "complete" && recorder.Code != http.StatusOK {
				t.Fatalf("completed search status = %d, body %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCodexAlphaSearchConcurrencyTransportFailureReleases(t *testing.T) {
	server := newTestServer(t)
	manager := server.handlers.AuthManager
	credential := &auth.Auth{
		ID: "alpha-limited", Provider: "codex", Status: auth.StatusActive,
		Metadata: map[string]any{"access_token": "synthetic-alpha-token", "concurrency_limit": 1},
	}
	if _, errRegister := manager.Register(context.Background(), credential); errRegister != nil {
		t.Fatal(errRegister)
	}
	executor := &alphaConcurrencyExecutor{serve: func(_ context.Context, _ *auth.Auth, _ *http.Request) (*http.Response, error) {
		if inflight, _ := manager.CredentialConcurrencySnapshot(credential.ID); inflight != 1 {
			t.Fatalf("upstream request inflight = %d, want 1", inflight)
		}
		return nil, errors.New("synthetic transport failure")
	}}
	manager.RegisterExecutor(executor)
	for attempt := 0; attempt < 2; attempt++ {
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, alphaConcurrencyRequest(context.Background()))
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("transport error status = %d, body %s", recorder.Code, recorder.Body.String())
		}
		if inflight, _ := manager.CredentialConcurrencySnapshot(credential.ID); inflight != 0 {
			t.Fatalf("transport failure retained %d reservations", inflight)
		}
	}
}
