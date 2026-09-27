package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

type lifecycleProxyExecutor struct {
	credentialConcurrencyExecutor
	prepare func(context.Context, *Auth) (*Auth, error)
	refresh func(context.Context, *Auth) (*Auth, error)
}

func (e *lifecycleProxyExecutor) ShouldPrepareRequestAuth(*Auth) bool { return e.prepare != nil }
func (e *lifecycleProxyExecutor) PrepareRequestAuth(ctx context.Context, a *Auth) (*Auth, error) {
	return e.prepare(ctx, a)
}
func (e *lifecycleProxyExecutor) Refresh(ctx context.Context, a *Auth) (*Auth, error) {
	if e.refresh != nil {
		return e.refresh(ctx, a)
	}
	return a, nil
}

func TestRequestProxyManagerLifecyclePolicy(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		for _, policy := range []string{"invalid", "pinned-conflict", "selected-conflict", "same", "unconfigured", "inherit-cleared", "body-ignored"} {
			t.Run(mode+"/"+policy, func(t *testing.T) {
				m, _, auths, models := newCredentialConcurrencyManager(t, 2, 3)
				current, _ := m.GetByID(auths[0].ID)
				current.ProxyURL = "http://credential.invalid:8080"
				if policy == "unconfigured" || policy == "inherit-cleared" || policy == "body-ignored" {
					current.ProxyURL = ""
				}
				if _, err := m.Update(context.Background(), current); err != nil {
					t.Fatal(err)
				}
				plugin := &fakePluginScheduler{handled: true, resp: pluginapi.SchedulerPickResponse{Handled: true, AuthID: auths[0].ID}}
				m.SetPluginScheduler(plugin)
				executor := &lifecycleProxyExecutor{credentialConcurrencyExecutor: credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "test"}}}
				calls, prepares := 0, 0
				var observed string
				executor.prepare = func(ctx context.Context, a *Auth) (*Auth, error) {
					prepares++
					if cliproxyexecutor.RequestProxyURL(ctx) != "" {
						t.Fatal("execution override leaked into credential preparation")
					}
					return a, nil
				}
				executor.execute = func(ctx context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					calls++
					observed = cliproxyexecutor.RequestProxyURL(ctx)
					if a.ID != auths[0].ID {
						t.Fatal("proxy conflict rerouted to another credential")
					}
					return cliproxyexecutor.Response{}, nil
				}
				executor.stream = func(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					_, err := executor.execute(ctx, a, r, o)
					if err != nil {
						return nil, err
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				}
				m.RegisterExecutor(executor)
				opts := cliproxyexecutor.Options{ProxyURL: "http://request.invalid:8081", Metadata: map[string]any{}}
				wantCalls, wantPicks := 1, 1
				switch policy {
				case "invalid":
					opts.ProxyURL = "socks5://"
					wantCalls, wantPicks = 0, 0
				case "pinned-conflict":
					opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = auths[0].ID
					wantCalls, wantPicks = 0, 0
				case "selected-conflict":
					wantCalls = 0
				case "same":
					opts.ProxyURL = current.ProxyURL
				case "inherit-cleared", "body-ignored":
					opts.ProxyURL = ""
				}
				ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), "http://parent.invalid:9000")
				req := cliproxyexecutor.Request{Model: models[0], Payload: []byte(`{"proxy_url":"http://body.invalid:9001"}`)}
				var errRun error
				switch mode {
				case "execute":
					_, errRun = m.Execute(ctx, []string{"test"}, req, opts)
				case "count":
					_, errRun = m.ExecuteCount(ctx, []string{"test"}, req, opts)
				case "stream":
					var stream *cliproxyexecutor.StreamResult
					stream, errRun = m.ExecuteStream(ctx, []string{"test"}, req, opts)
					if errRun == nil {
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								errRun = chunk.Err
							}
						}
					}
				}
				if wantCalls == 0 {
					var policyErr *proxyutil.ExecutionProxyError
					if !errors.As(errRun, &policyErr) || statusCodeFromError(errRun) != 400 {
						t.Fatalf("conflict error=%v, want typed request400", errRun)
					}
				} else if errRun != nil {
					t.Fatal(errRun)
				}
				if calls != wantCalls || prepares != wantCalls || plugin.calls != wantPicks {
					t.Fatalf("execution/preparation/plugin calls=%d/%d/%d want%d/%d/%d", calls, prepares, plugin.calls, wantCalls, wantCalls, wantPicks)
				}
				if wantCalls > 0 && observed != opts.ProxyURL {
					t.Fatalf("execution override=%q, want %q", observed, opts.ProxyURL)
				}
				for _, a := range auths {
					state, _ := m.GetByID(a.ID)
					if state.Failed != 0 || state.Unavailable || state.Quota.Exceeded {
						t.Fatalf("request conflict mutated provider state: %+v", state)
					}
					requireCredentialConcurrencySnapshot(t, m, a.ID, 0, 3)
				}
				if cliproxyexecutor.RequestProxyURL(ctx) != "http://parent.invalid:9000" {
					t.Fatal("child execution mutated parent proxy context")
				}
			})
		}
	}
}

func TestRequestProxyRefreshAndHomePreparationAreIsolated(t *testing.T) {
	m, _, auths, _ := newCredentialConcurrencyManager(t, 1, 3)
	executor := &lifecycleProxyExecutor{credentialConcurrencyExecutor: credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "test"}}}
	type marker struct{}
	ctx := context.WithValue(context.Background(), marker{}, "kept")
	ctx = cliproxyexecutor.WithRequestProxyURL(ctx, "http://request.invalid:8080")
	calls := 0
	observe := func(refreshCtx context.Context, a *Auth) (*Auth, error) {
		calls++
		if cliproxyexecutor.RequestProxyURL(refreshCtx) != "" || refreshCtx.Value(marker{}) != "kept" {
			t.Fatal("credential lifecycle context is not isolated")
		}
		return a, nil
	}
	executor.refresh = observe
	executor.prepare = observe
	m.RegisterExecutor(executor)
	if _, err := m.refreshAuthForRequest(ctx, auths[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PrepareRequestAuth(ctx, executor, auths[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := m.prepareHomeAuthSnapshot(ctx, executor, auths[0]); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || cliproxyexecutor.RequestProxyURL(ctx) == "" {
		t.Fatalf("lifecycle calls=%d or original execution proxy was cleared", calls)
	}
}

func TestRequestProxyTransportPolicyErrorDoesNotCountProviderFailure(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream", "stream-bootstrap", "stream-chunk"} {
		t.Run(mode, func(t *testing.T) {
			m, executor, auths, models := newCredentialConcurrencyManager(t, 2, 3)
			calls := 0
			_, conflict := proxyutil.ResolveExecutionProxy("direct", "http://credential.invalid:8080", "")
			executor.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls++
				return cliproxyexecutor.Response{}, fmt.Errorf("request transport: %w", conflict)
			}
			executor.stream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				calls++
				if mode == "stream" {
					return nil, conflict
				}
				chunks := make(chan cliproxyexecutor.StreamChunk, 2)
				if mode != "stream-bootstrap" {
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("started")}
				}
				chunks <- cliproxyexecutor.StreamChunk{Err: conflict}
				close(chunks)
				return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
			}
			var errRun error
			switch mode {
			case "execute":
				_, errRun = m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, cliproxyexecutor.Options{})
			case "count":
				_, errRun = m.ExecuteCount(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, cliproxyexecutor.Options{})
			default:
				var stream *cliproxyexecutor.StreamResult
				stream, errRun = m.ExecuteStream(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, cliproxyexecutor.Options{})
				if errRun == nil {
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							errRun = chunk.Err
						}
					}
				}
			}
			var policyErr *proxyutil.ExecutionProxyError
			if !errors.As(errRun, &policyErr) {
				t.Fatalf("lost policy error: %v", errRun)
			}
			if calls != 1 {
				t.Fatalf("policy failure retried %d executor calls", calls)
			}
			for _, a := range auths {
				state, _ := m.GetByID(a.ID)
				if state.Failed != 0 || state.Unavailable || state.Quota.Exceeded || len(state.ModelStates) != 0 {
					t.Fatalf("policy error recorded as provider failure: %+v", state)
				}
				requireCredentialConcurrencySnapshot(t, m, a.ID, 0, 3)
			}
		})
	}
}

func TestRequestProxyRechecksProxyAfterCredentialPreparation(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			m, _, auths, models := newCredentialConcurrencyManager(t, 2, 3)
			executor := &lifecycleProxyExecutor{credentialConcurrencyExecutor: credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "test"}}}
			prepared := 0
			executor.prepare = func(ctx context.Context, a *Auth) (*Auth, error) {
				prepared++
				current, _ := m.GetByID(a.ID)
				current.ProxyURL = "http://new-credential.invalid:8080"
				if _, err := m.Update(ctx, current); err != nil {
					t.Fatal(err)
				}
				return a, nil
			}
			executor.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				t.Fatal("changed credential proxy reached inference")
				return cliproxyexecutor.Response{}, nil
			}
			executor.stream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				t.Fatal("changed credential proxy reached streaming inference")
				return nil, nil
			}
			m.RegisterExecutor(executor)
			plugin := &fakePluginScheduler{handled: true, resp: pluginapi.SchedulerPickResponse{Handled: true, AuthID: auths[0].ID}}
			m.SetPluginScheduler(plugin)
			request := cliproxyexecutor.Request{Model: models[0]}
			options := cliproxyexecutor.Options{ProxyURL: "http://request.invalid:9000"}
			var errRun error
			switch mode {
			case "execute":
				_, errRun = m.Execute(context.Background(), []string{"test"}, request, options)
			case "count":
				_, errRun = m.ExecuteCount(context.Background(), []string{"test"}, request, options)
			case "stream":
				_, errRun = m.ExecuteStream(context.Background(), []string{"test"}, request, options)
			}
			var policyErr *proxyutil.ExecutionProxyError
			if !errors.As(errRun, &policyErr) || prepared != 1 || plugin.calls != 1 {
				t.Fatalf("changed proxy error/prepares/plugin=%v/%d/%d", errRun, prepared, plugin.calls)
			}
			state, _ := m.GetByID(auths[0].ID)
			if state.ProxyURL != "http://new-credential.invalid:8080" || state.Failed != 0 || state.Quota.Exceeded {
				t.Fatalf("credential policy change lost or cooled: %+v", state)
			}
			for _, a := range auths {
				requireCredentialConcurrencySnapshot(t, m, a.ID, 0, 3)
			}
		})
	}
}
