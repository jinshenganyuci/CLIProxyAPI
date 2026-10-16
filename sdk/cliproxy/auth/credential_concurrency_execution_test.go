package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type credentialConcurrencyExecutor struct {
	schedulerTestExecutor
	execute func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	stream  func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func (e *credentialConcurrencyExecutor) Execute(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.execute != nil {
		return e.execute(ctx, a, r, o)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}
func (e *credentialConcurrencyExecutor) CountTokens(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, a, r, o)
}
func (e *credentialConcurrencyExecutor) ExecuteStream(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.stream != nil {
		return e.stream(ctx, a, r, o)
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

type credentialConcurrencyPlugin struct {
	mu         sync.Mutex
	calls      int
	allowed    map[string]map[string]bool
	reject     error
	beforePick func(pluginapi.SchedulerPickRequest)
	delegate   bool
}

func (p *credentialConcurrencyPlugin) PickAuth(ctx context.Context, r pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.reject != nil {
		return pluginapi.SchedulerPickResponse{}, true, p.reject
	}
	if p.beforePick != nil {
		p.beforePick(r)
	}
	scope, _ := r.Options.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	candidates := append([]pluginapi.SchedulerAuthCandidate(nil), r.Candidates...)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, a := range candidates {
		if p.allowed[scope][a.ID] {
			return pluginapi.SchedulerPickResponse{Handled: true, AuthID: a.ID}, true, nil
		}
	}
	if p.delegate {
		return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst}, true, nil
	}
	return pluginapi.SchedulerPickResponse{}, true, &Error{Code: "auth_not_found", Message: "native binding has no available credential", HTTPStatus: 503}
}
func (p *credentialConcurrencyPlugin) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func newCredentialConcurrencyManager(t *testing.T, n, limit int) (*Manager, *credentialConcurrencyExecutor, []*Auth, []string) {
	t.Helper()
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.SetRetryConfig(0, 0, 0)
	e := &credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "test"}}
	m.RegisterExecutor(e)
	models := []string{"concurrency-a-" + uuid.NewString(), "concurrency-b-" + uuid.NewString()}
	auths := make([]*Auth, 0, n)
	for i := 0; i < n; i++ {
		a := &Auth{ID: fmt.Sprintf("concurrency-%d-%s", i, uuid.NewString()), Provider: "test", Status: StatusActive, Metadata: map[string]any{}}
		if limit > 0 {
			a.Metadata["concurrency_limit"] = limit
		}
		registered, err := m.Register(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		auths = append(auths, registered)
		registry.GetGlobalRegistry().RegisterClient(a.ID, "test", []*registry.ModelInfo{{ID: models[0]}, {ID: models[1]}})
		id := a.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return m, e, auths, models
}
func credentialConcurrencyOptions(scope string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: scope}}
}
func requireCredentialConcurrencySnapshot(t *testing.T, m *Manager, id string, inflight, limit int) {
	t.Helper()
	got, gotLimit := m.CredentialConcurrencySnapshot(id)
	if got != inflight || gotLimit != limit {
		t.Fatalf("snapshot %s = %d/%d, want %d/%d", id, got, gotLimit, inflight, limit)
	}
}
func awaitCredentialConcurrencySignal[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for deterministic test synchronization")
		var zero T
		return zero
	}
}

func TestCredentialConcurrencyThreeCredentialsThreeKeysMixedExecution(t *testing.T) {
	m, e, auths, models := newCredentialConcurrencyManager(t, 3, 3)
	p := &credentialConcurrencyPlugin{allowed: map[string]map[string]bool{}}
	for _, key := range []string{"key-a", "key-b", "key-c"} {
		p.allowed[key] = map[string]bool{}
		for _, a := range auths {
			p.allowed[key][a.ID] = true
		}
	}
	m.SetPluginScheduler(p)
	entered := make(chan string, 12)
	blocks := make([]chan struct{}, 9)
	done := make([]chan error, 9)
	e.execute = func(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		entered <- a.ID
		i := o.Metadata["slot"].(int)
		select {
		case <-blocks[i]:
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		case <-ctx.Done():
			return cliproxyexecutor.Response{}, ctx.Err()
		}
	}
	e.stream = func(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		entered <- a.ID
		i := o.Metadata["slot"].(int)
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: started\n\n")}
		go func() {
			defer close(chunks)
			select {
			case <-blocks[i]:
			case <-ctx.Done():
			}
		}()
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	for i := 0; i < 9; i++ {
		blocks[i] = make(chan struct{})
		done[i] = make(chan error, 1)
		i := i
		go func() {
			opts := credentialConcurrencyOptions([]string{"key-a", "key-b", "key-c"}[i%3])
			opts.Metadata["slot"] = i
			req := cliproxyexecutor.Request{Model: models[i%2]}
			var err error
			switch i % 3 {
			case 0:
				_, err = m.Execute(context.Background(), []string{"test"}, req, opts)
			case 1:
				_, err = m.ExecuteCount(context.Background(), []string{"test"}, req, opts)
			case 2:
				var result *cliproxyexecutor.StreamResult
				result, err = m.ExecuteStream(context.Background(), []string{"test"}, req, opts)
				if err == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
			}
			done[i] <- err
		}()
		awaitCredentialConcurrencySignal(t, entered)
	}
	for _, a := range auths {
		requireCredentialConcurrencySnapshot(t, m, a.ID, 3, 3)
	}
	_, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, credentialConcurrencyOptions("key-a"))
	if !IsCredentialConcurrencyError(err) || statusCodeFromError(err) != 429 {
		t.Fatalf("overflow = %v, want local 429", err)
	}
	if got := p.callCount(); got != 10 {
		t.Fatalf("plugin picks = %d, want one per request", got)
	}
	for _, a := range auths {
		current, _ := m.GetByID(a.ID)
		if current.Failed != 0 || current.Unavailable || current.Quota.Exceeded || !current.NextRetryAfter.IsZero() {
			t.Fatalf("busy damaged credential state: %#v", current)
		}
	}
	for i := range blocks {
		close(blocks[i])
		if errDone := awaitCredentialConcurrencySignal(t, done[i]); errDone != nil {
			t.Fatal(errDone)
		}
	}
	for _, a := range auths {
		requireCredentialConcurrencySnapshot(t, m, a.ID, 0, 3)
	}
}

func TestCredentialConcurrencyDefaultTenAndPinnedLimit(t *testing.T) {
	for _, limit := range []int{0, 3} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			m, e, auths, models := newCredentialConcurrencyManager(t, 2, limit)
			want := limit
			if want == 0 {
				want = 10
			}
			releases := make([]func(), 0, want)
			for i := 0; i < want; i++ {
				release, err := m.AcquireCredentialConcurrency(context.Background(), auths[0])
				if err != nil {
					t.Fatal(err)
				}
				releases = append(releases, release)
			}
			defer func() {
				for _, release := range releases {
					release()
				}
			}()
			e.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				t.Fatal("pinned overflow reached executor")
				return cliproxyexecutor.Response{}, nil
			}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: auths[0].ID}}
			_, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, opts)
			if !IsCredentialConcurrencyError(err) {
				t.Fatalf("pinned overflow = %v", err)
			}
			requireCredentialConcurrencySnapshot(t, m, auths[1].ID, 0, want)
		})
	}
}

func TestCredentialConcurrencyBusyDoesNotConsumeCredentialRetryBudget(t *testing.T) {
	m, e, auths, models := newCredentialConcurrencyManager(t, 2, 1)
	m.SetRetryConfig(20, 1, 1)
	release, err := m.AcquireCredentialConcurrency(context.Background(), auths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	e.execute = func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		if a.ID != auths[1].ID {
			t.Fatalf("executed %s", a.ID)
		}
		return cliproxyexecutor.Response{}, nil
	}
	if _, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialConcurrencyPluginBoundariesAndErrorPriority(t *testing.T) {
	for _, tc := range []struct {
		name       string
		busy       bool
		reject     error
		wantStatus int
		wantBusy   bool
	}{
		{name: "bound_busy", busy: true, wantStatus: 429, wantBusy: true},
		{name: "no_matching_binding", wantStatus: 503},
		{name: "model_forbidden", busy: true, reject: &Error{Code: "model_not_allowed", Message: "denied", HTTPStatus: 403}, wantStatus: 403},
		{name: "quota_exceeded", busy: true, reject: &Error{Code: "rpm_exceeded", Message: "quota", HTTPStatus: 429}, wantStatus: 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, e, auths, models := newCredentialConcurrencyManager(t, 3, 1)
			p := &credentialConcurrencyPlugin{allowed: map[string]map[string]bool{"key-a": {auths[0].ID: true}}, reject: tc.reject}
			m.SetPluginScheduler(p)
			if tc.busy {
				release, err := m.AcquireCredentialConcurrency(context.Background(), auths[0])
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			} else {
				p.allowed["key-a"] = map[string]bool{}
			}
			e.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				t.Fatal("execution escaped binding")
				return cliproxyexecutor.Response{}, nil
			}
			_, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, credentialConcurrencyOptions("key-a"))
			if statusCodeFromError(err) != tc.wantStatus || IsCredentialConcurrencyError(err) != tc.wantBusy {
				t.Fatalf("error=%v status=%d", err, statusCodeFromError(err))
			}
			if p.callCount() != 1 {
				t.Fatalf("plugin calls=%d", p.callCount())
			}
		})
	}
}

func TestCredentialConcurrencyPluginAdmissionRaceDoesNotChargeTwice(t *testing.T) {
	m, e, auths, models := newCredentialConcurrencyManager(t, 2, 1)
	p := &credentialConcurrencyPlugin{allowed: map[string]map[string]bool{"key-a": {auths[0].ID: true, auths[1].ID: true}}}
	var competingRelease func()
	p.beforePick = func(req pluginapi.SchedulerPickRequest) {
		var err error
		competingRelease, err = m.AcquireCredentialConcurrency(context.Background(), auths[0])
		if err != nil {
			t.Fatal(err)
		}
	}
	m.SetPluginScheduler(p)
	e.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		t.Fatal("lost admission race reached executor")
		return cliproxyexecutor.Response{}, nil
	}
	_, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, credentialConcurrencyOptions("key-a"))
	if competingRelease != nil {
		defer competingRelease()
	}
	if !IsCredentialConcurrencyError(err) {
		t.Fatal(err)
	}
	if p.callCount() != 1 {
		t.Fatalf("plugin charged %d times", p.callCount())
	}
	requireCredentialConcurrencySnapshot(t, m, auths[1].ID, 0, 1)
}

func TestCredentialConcurrencyPluginSkipsFullHigherPriorityAndDelegate(t *testing.T) {
	for _, delegate := range []bool{false, true} {
		t.Run(fmt.Sprint(delegate), func(t *testing.T) {
			m, e, auths, models := newCredentialConcurrencyManager(t, 2, 1)
			a, _ := m.GetByID(auths[0].ID)
			a.Attributes = map[string]string{"priority": "100"}
			if _, err := m.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			release, err := m.AcquireCredentialConcurrency(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			p := &credentialConcurrencyPlugin{allowed: map[string]map[string]bool{"key-a": {auths[1].ID: true}}, delegate: delegate}
			if delegate {
				p.allowed = nil
			}
			m.SetPluginScheduler(p)
			e.execute = func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if a.ID != auths[1].ID {
					t.Fatalf("executed full high-priority credential %s", a.ID)
				}
				return cliproxyexecutor.Response{}, nil
			}
			if _, err := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, credentialConcurrencyOptions("key-a")); err != nil {
				t.Fatal(err)
			}
			if p.callCount() != 1 {
				t.Fatalf("plugin calls=%d", p.callCount())
			}
		})
	}
}

func TestCredentialConcurrencyStreamTerminalErrorReleasesBeforeConsumerRetry(t *testing.T) {
	m, _, auths, _ := newCredentialConcurrencyManager(t, 1, 1)
	lease, err := m.acquireCredentialConcurrency(context.Background(), auths[0])
	if err != nil {
		t.Fatal(err)
	}
	upstreamCtx, cancel := context.WithCancel(context.Background())
	lease.cancel = cancel
	source := make(chan cliproxyexecutor.StreamChunk, 1)
	source <- cliproxyexecutor.StreamChunk{Err: errors.New("terminal stream error")}
	streamCtx := context.WithValue(upstreamCtx, credentialConcurrencyLeaseContextKey{}, lease)
	wrapped := m.wrapStreamResult(streamCtx, auths[0], "test", "model", "model", nil, nil, source, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{}, m.nextExecutionSeq())
	chunk := awaitCredentialConcurrencySignal(t, wrapped.Chunks)
	if chunk.Err == nil {
		t.Fatal("terminal error disappeared")
	}
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 1)
	awaitCredentialConcurrencySignal(t, upstreamCtx.Done())
	release, err := m.AcquireCredentialConcurrency(context.Background(), auths[0])
	if err != nil {
		t.Fatalf("immediate same-request retry rejected: %v", err)
	}
	release()
	close(source)
	for range wrapped.Chunks {
	}
}

func TestCredentialConcurrencyStreamCancellationStopsSilentUpstream(t *testing.T) {
	m, e, auths, models := newCredentialConcurrencyManager(t, 1, 1)
	stopped := make(chan struct{})
	e.stream = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("started")}
		go func() { <-ctx.Done(); close(stopped); close(chunks) }()
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result, err := m.ExecuteStream(ctx, []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	awaitCredentialConcurrencySignal(t, result.Chunks)
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 1, 1)
	cancel()
	awaitCredentialConcurrencySignal(t, stopped)
	for range result.Chunks {
	}
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 1)
}

func TestCredentialConcurrencyBusyNeverRetriesOrActivatesCredits(t *testing.T) {
	m, _, _, _ := newCredentialConcurrencyManager(t, 1, 1)
	err := newCredentialConcurrencyError()
	wait, retry := m.shouldRetryAfterErrorWithAttempted(context.Background(), cliproxyexecutor.Options{}, err, 0, []string{"test"}, "model", time.Minute, -1, 999, nil)
	if retry || wait != 0 {
		t.Fatalf("local busy retried wait=%v retry=%v", wait, retry)
	}
	if shouldAttemptAntigravityCreditsFallback(m, err, []string{"antigravity"}) {
		t.Fatal("local busy activated paid credits")
	}
	if statusCodeFromError(err) != http.StatusTooManyRequests {
		t.Fatal(err)
	}
}

type credentialConcurrencyRPCError struct {
	code   string
	status int
}

func (e credentialConcurrencyRPCError) Error() string     { return "plugin rejected request" }
func (e credentialConcurrencyRPCError) ErrorCode() string { return e.code }
func (e credentialConcurrencyRPCError) StatusCode() int   { return e.status }

func TestCredentialConcurrencyPluginRPCErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		code           string
		status         int
		filtered, busy bool
	}{
		{"auth_not_found", 503, true, true}, {"auth_unavailable", 503, true, true},
		{"auth_not_found", 503, false, false}, {"model_not_allowed", 403, true, false},
		{"rpm_exceeded", 429, true, false}, {"auth_not_found", 403, true, false},
		{"auth_not_found", 429, true, false}, {"plugin_transport", 503, true, false},
	} {
		t.Run(fmt.Sprintf("%s-%d-%t", tc.code, tc.status, tc.filtered), func(t *testing.T) {
			original := fmt.Errorf("scheduler RPC: %w", credentialConcurrencyRPCError{code: tc.code, status: tc.status})
			err := credentialConcurrencySelectionError(original, tc.filtered)
			if IsCredentialConcurrencyError(err) != tc.busy {
				t.Fatalf("classified %v as local busy=%t", err, IsCredentialConcurrencyError(err))
			}
			if !tc.busy && err != original {
				t.Fatal("changed an unrelated plugin error")
			}
		})
	}
	m, _, auths, models := newCredentialConcurrencyManager(t, 2, 1)
	release, err := m.AcquireCredentialConcurrency(context.Background(), auths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	p := &credentialConcurrencyPlugin{reject: credentialConcurrencyRPCError{code: "auth_not_found", status: 503}}
	m.SetPluginScheduler(p)
	_, err = m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, credentialConcurrencyOptions("key-a"))
	if !IsCredentialConcurrencyError(err) || p.callCount() != 1 {
		t.Fatalf("real RPC-shaped error=%v, plugin calls=%d", err, p.callCount())
	}
}
