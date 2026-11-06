package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type v858RequestScoped400 struct{}

func (v858RequestScoped400) Error() string         { return "synthetic request capability rejected" }
func (v858RequestScoped400) StatusCode() int       { return http.StatusBadRequest }
func (v858RequestScoped400) IsRequestScoped() bool { return true }

func runV858Execution(ctx context.Context, m *Manager, mode, provider, model string, opts cliproxyexecutor.Options) error {
	req := cliproxyexecutor.Request{Model: model}
	switch mode {
	case "execute":
		_, err := m.Execute(ctx, []string{provider}, req, opts)
		return err
	case "count":
		_, err := m.ExecuteCount(ctx, []string{provider}, req, opts)
		return err
	default:
		opts.Stream = true
		stream, err := m.ExecuteStream(ctx, []string{provider}, req, opts)
		if err != nil {
			return err
		}
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	}
}

func TestV858TerminalAndRequest400PreserveForkBindingLeaseAndReplacement(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream", "bootstrap"} {
		for _, kind := range []string{"terminal502", "wrapped401", "request400"} {
			for _, replace := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/replace=%t", mode, kind, replace), func(t *testing.T) {
					ctx := WithSkipPersist(t.Context())
					m, _, auths, models := newCredentialConcurrencyManager(t, 2, 1)
					selected := auths[0].Clone()
					selected.ProxyURL = "direct"
					selected.Metadata["access_token"] = "synthetic-old-token"
					selected.Metadata["codex_identity_namespace"] = "old-fixture-namespace"
					var err error
					selected, err = m.Update(ctx, selected)
					if err != nil {
						t.Fatal(err)
					}
					plugin := &credentialConcurrencyPlugin{allowed: map[string]map[string]bool{"bound-key": {selected.ID: true}}}
					m.SetPluginScheduler(plugin)
					hook := &resultCaptureHook{}
					m.hook = hook
					terminal := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusBadGateway, Header: http.Header{"X-Fixture": {"preserved"}}, Body: []byte(`{"detail":"synthetic terminal outcome"}`)}
					var failure error = terminal
					if kind == "wrapped401" {
						terminal.HTTPStatus = http.StatusUnauthorized
						failure = fmt.Errorf("wrapped: %w", terminal)
					} else if kind == "request400" {
						failure = v858RequestScoped400{}
					}
					calls, preparations, refreshes := 0, 0, 0
					var replacement *Auth
					executor := &lifecycleProxyExecutor{credentialConcurrencyExecutor: credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "test"}}}
					executor.prepare = func(prepareCtx context.Context, a *Auth) (*Auth, error) {
						preparations++
						if cliproxyexecutor.RequestProxyURL(prepareCtx) != "" {
							t.Fatal("inference proxy leaked into credential preparation")
						}
						return a, nil
					}
					executor.refresh = func(_ context.Context, a *Auth) (*Auth, error) { refreshes++; return a, nil }
					executor.execute = func(execCtx context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						calls++
						if a.ID != selected.ID || a.ProxyURL != "direct" || a.Metadata["codex_identity_namespace"] != "old-fixture-namespace" || cliproxyexecutor.RequestProxyURL(execCtx) != "direct" {
							t.Fatal("bound identity or single-credential proxy changed")
						}
						requireCredentialConcurrencySnapshot(t, m, a.ID, 1, 1)
						if replace {
							next := a.Clone()
							next.ProxyURL = "http://replacement.invalid:8080"
							next.Metadata["access_token"] = "synthetic-replacement-token"
							next.Metadata["codex_identity_namespace"] = "replacement-fixture-namespace"
							m.Remove(ctx, a.ID)
							replacement, err = m.Register(ctx, next)
							if err != nil {
								t.Fatal(err)
							}
							registry.GetGlobalRegistry().RegisterClient(a.ID, "test", []*registry.ModelInfo{{ID: models[0]}, {ID: models[1]}})
						}
						return cliproxyexecutor.Response{}, failure
					}
					executor.stream = func(execCtx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						_, errExecute := executor.execute(execCtx, a, req, opts)
						if mode != "bootstrap" {
							return nil, errExecute
						}
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Err: errExecute}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					}
					m.RegisterExecutor(executor)
					opts := credentialConcurrencyOptions("bound-key")
					opts.ProxyURL = "direct"
					err = runV858Execution(ctx, m, mode, "test", models[0], opts)
					if !errors.Is(err, failure) || calls != 1 || preparations != 1 || refreshes != 0 || plugin.callCount() != 1 {
						t.Fatalf("request crossed fork boundaries: err=%v calls=%d prepares=%d refreshes=%d picks=%d", err, calls, preparations, refreshes, plugin.callCount())
					}
					if kind != "request400" {
						requireTerminalResponse(t, err, terminal)
					}
					for _, a := range auths {
						current, _ := m.GetByID(a.ID)
						requireCredentialConcurrencySnapshot(t, m, a.ID, 0, 1)
						if current.Unavailable || current.Quota.Exceeded || !current.NextRetryAfter.IsZero() {
							t.Fatal("request-owned failure cooled a credential")
						}
						if a.ID != selected.ID && current.Failed != 0 {
							t.Fatal("request-owned failure touched a sibling credential")
						}
					}
					current, _ := m.GetByID(selected.ID)
					if replace && (current.RegistrationEpoch != replacement.RegistrationEpoch || current.Generation != replacement.Generation || current.Failed != replacement.Failed || current.Metadata["access_token"] != "synthetic-replacement-token" || current.Metadata["codex_identity_namespace"] != "replacement-fixture-namespace" || current.ProxyURL != replacement.ProxyURL) {
						t.Fatal("old result overwrote replacement credential state")
					}
					results := hook.Results()
					if len(results) != 1 || results[0].RegistrationEpoch != selected.RegistrationEpoch || results[0].Error == nil || results[0].Error.Code != requestScopedErrorCode {
						t.Fatalf("request failure audit lost classification or original epoch: %#v", results)
					}
				})
			}
		}
	}
}

func TestV858PluginTerminateCannotBecomeConfiguredCredentialFailover(t *testing.T) {
	for _, home := range []bool{false, true} {
		for _, mode := range []string{"execute", "count", "stream", "bootstrap"} {
			t.Run(fmt.Sprintf("home=%t/%s", home, mode), func(t *testing.T) {
				m, _, auths, models := newCredentialConcurrencyManager(t, 2, 1)
				m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: home}, CredentialConcurrency: internalconfig.CredentialConcurrencyConfig{CPACancelBound: time.Nanosecond}})
				rules := []internalconfig.RequestScopedErrorRule{{Status: 403, Match: []string{"synthetic plugin denial"}, Action: RequestScopedActionContinueAndCooldown}}
				for _, a := range auths {
					a.Metadata["request_scoped_errors"] = rules
					if _, err := m.Update(t.Context(), a); err != nil {
						t.Fatal(err)
					}
				}
				plugin := &credentialConcurrencyPlugin{allowed: map[string]map[string]bool{"bound-key": {auths[0].ID: true, auths[1].ID: true}}}
				m.SetPluginScheduler(plugin)
				executions := executionregistry.New()
				releases := make(chan executionregistry.ReleaseGroup, 4)
				dispatcher := &accountedHomeExecutionDispatcher{auths: []Auth{*auths[0], *auths[1]}}
				if home {
					executions.SetReleaseSink(func(group executionregistry.ReleaseGroup, sequence int64) *executionregistry.ReleaseTicket {
						releases <- group
						return executionregistry.NewReleaseTicket(group, sequence, make(chan struct{}))
					})
					m.PublishHomeDispatch(dispatcher, executions, 1)
				}
				upstreamCalls, interceptions := 0, 0
				executor := &credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "test"}}
				executor.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					upstreamCalls++
					return cliproxyexecutor.Response{}, nil
				}
				executor.stream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					upstreamCalls++
					ch := make(chan cliproxyexecutor.StreamChunk)
					close(ch)
					return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
				}
				m.RegisterExecutor(executor)
				opts := credentialConcurrencyOptions("bound-key")
				opts.RequestAfterAuthInterceptor = func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					interceptions++
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{Terminate: true, StatusCode: 403, ResponseHeaders: http.Header{"X-Plugin": {"stop"}}, ResponseBody: []byte("synthetic plugin denial")}
				}
				err := runV858Execution(t.Context(), m, mode, "test", models[0], opts)
				var terminal *cliproxyexecutor.RequestTerminatedError
				if !errors.As(err, &terminal) || terminal.HTTPStatus != 403 || string(terminal.Body) != "synthetic plugin denial" || terminal.Header.Get("X-Plugin") != "stop" || upstreamCalls != 0 || interceptions != 1 {
					t.Fatalf("plugin denial replayed or replaced: err=%v upstream=%d intercepted=%d", err, upstreamCalls, interceptions)
				}
				for _, a := range auths {
					current, _ := m.GetByID(a.ID)
					if current.Failed != 0 || current.Unavailable || current.Quota.Exceeded {
						t.Fatal("plugin denial mutated credential availability")
					}
					requireCredentialConcurrencySnapshot(t, m, a.ID, 0, 1)
				}
				if home {
					if dispatcher.calls.Load() != 1 || len(executions.FreezeInFlight(time.Time{}).Executions) != 0 || len(releases) != 1 {
						t.Fatal("Home plugin rejection redispatched, leaked scope, or lost/doubled release")
					}
				} else if plugin.callCount() != 1 {
					t.Fatal("plugin scheduler picked more than once")
				}
			})
		}
	}
}

func TestV858CanceledTerminalStreamKeepsCallerCancellationAndReleasesLease(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		for _, bootstrap := range []bool{false, true} {
			t.Run(fmt.Sprintf("home=%t/bootstrap=%t", homeMode, bootstrap), func(t *testing.T) {
				m, executor, auths, models := newCredentialConcurrencyManager(t, 1, 1)
				m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: homeMode}})
				executions := executionregistry.New()
				if homeMode {
					m.PublishHomeDispatch(&accountedHomeExecutionDispatcher{auths: []Auth{*auths[0]}}, executions, 1)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				executor.stream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					cancel()
					failure := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: 502, Body: []byte("synthetic canceled terminal response")}
					if !bootstrap {
						return nil, failure
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Err: failure}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				}
				err := runV858Execution(ctx, m, "stream", "test", models[0], cliproxyexecutor.Options{})
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("terminal response replaced caller cancellation: %v", err)
				}
				requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 1)
				if len(executions.FreezeInFlight(time.Time{}).Executions) != 0 {
					t.Fatal("canceled Home selection leaked its execution scope")
				}
			})
		}
	}
}

type v858HomeUsageCapture struct {
	provider string
	records  chan coreusage.Record
}

func (c *v858HomeUsageCapture) HandleUsage(_ context.Context, record coreusage.Record) {
	if c.records != nil && record.Provider == c.provider {
		c.records <- record
	}
}

type v858RequestScoped401 struct{ v858RequestScoped400 }

func (v858RequestScoped401) StatusCode() int { return http.StatusUnauthorized }

func TestV858HomeCountTerminal401DoesNotPublishCredentialRejection(t *testing.T) {
	const provider = "v858-home-count-terminal"
	capture := &v858HomeUsageCapture{provider: provider, records: make(chan coreusage.Record, 32)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), &v858HomeUsageCapture{}) })
	for _, kind := range []string{"terminal", "request-scoped", "ordinary", "credential-scoped"} {
		t.Run(kind, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			m.SetRetryConfig(0, 0, 0)
			a := Auth{ID: "v858-count-" + kind, Provider: provider, Status: StatusActive, Metadata: map[string]any{"access_token": "synthetic-home-token", "codex_identity_namespace": "synthetic-home-namespace"}}
			dispatcher := &accountedHomeExecutionDispatcher{auths: []Auth{a}}
			executions := executionregistry.New()
			m.PublishHomeDispatch(dispatcher, executions, 1)
			terminal := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: 401, Body: []byte("synthetic terminal unauthorized")}
			var failure error = terminal
			switch kind {
			case "request-scoped":
				failure = v858RequestScoped401{}
			case "ordinary":
				failure = &Error{HTTPStatus: 401, Message: "synthetic invalid access token"}
			case "credential-scoped":
				failure = credentialTerminalResponseError{error: terminal}
			}
			calls := 0
			executor := &credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: provider}}
			executor.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls++
				return cliproxyexecutor.Response{}, failure
			}
			m.RegisterExecutor(executor)
			_, err := m.ExecuteCount(t.Context(), []string{provider}, cliproxyexecutor.Request{Model: "v858-count-model"}, cliproxyexecutor.Options{})
			if statusCodeFromError(err) != 401 || calls != 1 {
				t.Fatalf("wrong terminal count result: %v, calls=%d", err, calls)
			}
			// The usage queue is FIFO; this marker proves prior records have been
			// delivered without a timing sleep or changing the global dispatcher.
			barrier := "barrier-" + kind
			coreusage.PublishRecord(t.Context(), coreusage.Record{Provider: provider, Model: barrier})
			var records []coreusage.Record
			for {
				record := awaitCredentialConcurrencySignal(t, capture.records)
				if record.Model == barrier {
					break
				}
				records = append(records, record)
			}
			wantReports := 0
			if kind == "ordinary" || kind == "credential-scoped" {
				wantReports = 1
			}
			if len(records) != wantReports {
				t.Fatalf("Home credential-rejection records=%d, want %d for %s", len(records), wantReports, kind)
			}
			if wantReports == 1 && (records[0].AuthID != a.ID || records[0].ExecutorType != homeResultExecutorType || records[0].Fail.StatusCode != 401 || records[0].AccessTokenSHA256 != AccessTokenSHA256(&a)) {
				t.Fatal("ordinary credential rejection lost its token snapshot or identity")
			}
			if len(executions.FreezeInFlight(time.Time{}).Executions) != 0 {
				t.Fatal("Home CountTokens leaked its execution scope")
			}
		})
	}
}

type v858ModelScopedTerminalError struct{ error }

func (e v858ModelScopedTerminalError) Unwrap() error       { return e.error }
func (v858ModelScopedTerminalError) IsModelScoped() bool   { return true }
func (v858ModelScopedTerminalError) IsRequestScoped() bool { return false }
func (v858ModelScopedTerminalError) IsRequestStop() bool   { return true }

func TestV858ExplicitModelScopedTerminalKeepsQuotaAndStopsReplay(t *testing.T) {
	terminal := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: 429, Body: []byte("synthetic verified model quota")}
	failure := v858ModelScopedTerminalError{error: terminal}
	if isRequestScopedError(failure) || isCredentialScopedError(failure) || !isRequestStopError(failure) {
		t.Fatal("model quota must remain distinct from request failure, credential quota and replay policy")
	}
	result := resultErrorFromError(failure)
	if result.HTTPStatus != 429 || result.Code == requestScopedErrorCode || shouldSkipCredentialCooldown(result) {
		t.Fatal("explicit model quota was erased by terminal response inference")
	}
	for _, status := range []int{400, 401, 429} {
		opaque := fmt.Errorf("opaque wrapped response: %w", &cliproxyexecutor.RequestTerminatedError{HTTPStatus: status})
		if !isRequestScopedError(opaque) || !shouldSkipCredentialCooldown(resultErrorFromError(opaque)) {
			t.Fatalf("opaque terminal %d lost availability-neutral fallback", status)
		}
	}
}
