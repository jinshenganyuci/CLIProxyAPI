package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestDuplexAdmissionSharesThreeCredentialBudgetsAndKeys(t *testing.T) {
	m, executor, auths, models := newCredentialConcurrencyManager(t, 3, 0)
	limits := []int{3, 2, 10}
	keys := []string{"key-a", "key-b", "key-c"}
	plugin := &credentialConcurrencyPlugin{allowed: map[string]map[string]bool{}}
	for i, auth := range auths {
		plugin.allowed[keys[i]] = map[string]bool{auth.ID: true}
		if i < 2 {
			auth.Metadata["concurrency_limit"] = limits[i]
			var errUpdate error
			auths[i], errUpdate = m.Update(context.Background(), auth)
			if errUpdate != nil {
				t.Fatal(errUpdate)
			}
		}
	}
	m.SetPluginScheduler(plugin)
	type opened struct {
		ctx       context.Context
		admission *cliproxyexecutor.WebsocketResponseAdmission
	}
	openedStreams := make(chan opened, 3)
	executor.stream = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		openedStreams <- opened{ctx, cliproxyexecutor.WebsocketResponseAdmissionFromContext(ctx)}
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("started")}
		go func() { <-ctx.Done(); close(chunks) }()
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	sessions := make([]opened, 3)
	streams := make([]*cliproxyexecutor.StreamResult, 3)
	cancels := make([]context.CancelFunc, 3)
	releases := make([][]func(), 3)
	for i := range auths {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		t.Cleanup(cancel)
		ctx = cliproxyexecutor.WithWebsocketInput(ctx, make(chan cliproxyexecutor.WebsocketInput))
		stream, errStream := m.ExecuteStream(ctx, []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, credentialConcurrencyOptions(keys[i]))
		if errStream != nil {
			t.Fatal(errStream)
		}
		streams[i] = stream
		awaitCredentialConcurrencySignal(t, stream.Chunks)
		sessions[i] = awaitCredentialConcurrencySignal(t, openedStreams)
		admission := sessions[i].admission
		if admission == nil || admission.Acquire == nil || admission.ReleaseInitial == nil {
			t.Fatal("real Manager stream did not receive response admission")
		}
		requireCredentialConcurrencySnapshot(t, m, auths[i].ID, 1, limits[i])
		admission.ReleaseInitial()
		admission.ReleaseInitial()
		requireCredentialConcurrencySnapshot(t, m, auths[i].ID, 0, limits[i])
		if sessions[i].ctx.Err() != nil {
			t.Fatal("completing initial response canceled the duplex socket")
		}
		for range limits[i] {
			release, errAcquire := admission.Acquire(sessions[i].ctx)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			releases[i] = append(releases[i], release)
			t.Cleanup(release)
		}
		if release, errBusy := admission.Acquire(sessions[i].ctx); release != nil || !IsCredentialConcurrencyError(errBusy) {
			t.Fatalf("follow-up overflow = %v/%v, want typed local busy", release != nil, errBusy)
		}
		requireCredentialConcurrencySnapshot(t, m, auths[i].ID, limits[i], limits[i])
	}
	if plugin.callCount() != 18 {
		t.Fatalf("follow-up admission plugin/RPM count = %d, want one per admitted submission", plugin.callCount())
	}
	// Key B now binds the full A credential while B itself has spare capacity.
	// Admission must remain attached to the credential, never the original key.
	releases[1][0]()
	plugin.mu.Lock()
	plugin.allowed[keys[1]] = map[string]bool{auths[0].ID: true}
	plugin.mu.Unlock()
	_, errBusy := m.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{Model: models[1]}, credentialConcurrencyOptions(keys[1]))
	if !IsCredentialConcurrencyError(errBusy) || plugin.callCount() != 19 {
		t.Fatalf("cross-key busy/plugin calls = %v/%d", errBusy, plugin.callCount())
	}
	for i := range auths {
		state, _ := m.GetByID(auths[i].ID)
		if state.Failed != 0 || state.Quota.Exceeded || state.Unavailable || len(state.ModelStates) != 0 {
			t.Fatalf("local admission changed provider state: %+v", state)
		}
		for _, release := range releases[i] {
			release()
			release()
		}
		requireCredentialConcurrencySnapshot(t, m, auths[i].ID, 0, limits[i])
		cancels[i]()
		for range streams[i].Chunks {
		}
		requireCredentialConcurrencySnapshot(t, m, auths[i].ID, 0, limits[i])
	}
}

func TestDuplexAdmissionTracksLiveLimitAndRegistration(t *testing.T) {
	m, _, auths, _ := newCredentialConcurrencyManager(t, 1, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = cliproxyexecutor.WithWebsocketInput(ctx, make(chan cliproxyexecutor.WebsocketInput))
	initial, errAcquire := m.acquireCredentialConcurrency(ctx, auths[0])
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	defer initial.Release()
	admission := cliproxyexecutor.WebsocketResponseAdmissionFromContext(m.withWebsocketResponseAdmission(ctx, auths[0], initial, "test", "", cliproxyexecutor.Options{}))
	admission.ReleaseInitial()
	release, errAcquire := admission.Acquire(ctx)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	defer release()
	current, _ := m.GetByID(auths[0].ID)
	current.Metadata["concurrency_limit"] = 1
	if _, errUpdate := m.Update(ctx, current); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if _, errBusy := admission.Acquire(ctx); !IsCredentialConcurrencyError(errBusy) {
		t.Fatalf("updated capacity ignored: %v", errBusy)
	}
	release()
	current, _ = m.GetByID(auths[0].ID)
	current.Disabled = true
	if _, errUpdate := m.Update(ctx, current); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if _, errDisabled := admission.Acquire(ctx); errDisabled == nil {
		t.Fatal("disabled credential admitted another response")
	}
	m.Remove(ctx, auths[0].ID)
	if _, errRegister := m.Register(ctx, &Auth{ID: auths[0].ID, Provider: "test", Status: StatusActive, Metadata: map[string]any{"concurrency_limit": 1}}); errRegister != nil {
		t.Fatal(errRegister)
	}
	if _, errStale := admission.Acquire(ctx); errStale == nil {
		t.Fatal("old socket admitted a response using a re-registered credential")
	}
	m.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	if _, errHome := admission.Acquire(ctx); errHome == nil {
		t.Fatal("existing local socket bypassed the new Home admission owner")
	}
	m.SetConfig(&config.Config{})
	cancel()
	if _, errCanceled := admission.Acquire(ctx); !errors.Is(errCanceled, context.Canceled) {
		t.Fatalf("canceled admission = %v", errCanceled)
	}
	requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 1)
}

func TestDuplexLocalBusyStreamErrorDoesNotCoolCredential(t *testing.T) {
	for _, local := range []bool{true, false} {
		t.Run(fmt.Sprint(local), func(t *testing.T) {
			m, _, auths, models := newCredentialConcurrencyManager(t, 1, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lease, errAcquire := m.acquireCredentialConcurrency(ctx, auths[0])
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			ctx = context.WithValue(ctx, credentialConcurrencyLeaseContextKey{}, lease)
			var terminal error = &Error{Code: "upstream_rate_limit", HTTPStatus: 429, Message: `{"error":{"code":"credential_concurrency_exceeded"}}`}
			if local {
				terminal = fmt.Errorf("duplex successor admission: %w", newCredentialConcurrencyError())
			}
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			chunks <- cliproxyexecutor.StreamChunk{Err: terminal}
			close(chunks)
			stream := m.wrapStreamResult(ctx, auths[0], "test", models[0], models[0], nil, nil, chunks, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{})
			chunk := awaitCredentialConcurrencySignal(t, stream.Chunks)
			if IsCredentialConcurrencyError(chunk.Err) != local {
				t.Fatalf("typed error classification changed: %v", chunk.Err)
			}
			for range stream.Chunks {
			}
			state, _ := m.GetByID(auths[0].ID)
			if local {
				if state.Failed != 0 || state.Quota.Exceeded || state.Unavailable || len(state.ModelStates) != 0 {
					t.Fatalf("duplex local 429 became provider cooldown: %+v", state)
				}
			} else if state.Failed != 1 || !state.Quota.Exceeded || len(state.ModelStates) == 0 {
				t.Fatalf("real upstream 429 lost provider accounting: %+v", state)
			}
			requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 1)
		})
	}
}

func TestDuplexFollowupRechecksPluginPolicyWithoutRerouting(t *testing.T) {
	for _, policy := range []string{"allow", "unhandled", "delegate", "other-auth", "binding-changed", "model-denied", "rpm-denied", "disabled-during-check", "replaced-during-check", "home-enabled-during-check"} {
		t.Run(policy, func(t *testing.T) {
			m, executor, auths, models := newCredentialConcurrencyManager(t, 1, 3)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = cliproxyexecutor.WithWebsocketInput(ctx, make(chan cliproxyexecutor.WebsocketInput))
			var admission *cliproxyexecutor.WebsocketResponseAdmission
			executor.stream = func(streamCtx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				admission = cliproxyexecutor.WebsocketResponseAdmissionFromContext(streamCtx)
				chunks := make(chan cliproxyexecutor.StreamChunk, 1)
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("started")}
				go func() { <-streamCtx.Done(); close(chunks) }()
				return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
			}
			scheduler := &fakePluginScheduler{}
			var denied error
			scheduler.pick = func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
				if len(req.Candidates) != 1 || req.Candidates[0].ID != auths[0].ID || req.Model != models[0] || req.Options.Metadata[cliproxyexecutor.CallerScopeMetadataKey] != "synthetic-caller-scope" {
					t.Fatalf("lost fixed credential/model/caller policy context: %+v", req)
				}
				if scheduler.calls == 1 {
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: auths[0].ID}, true, nil
				}
				requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 1, 3)
				switch policy {
				case "unhandled":
					return pluginapi.SchedulerPickResponse{}, false, nil
				case "delegate":
					return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst}, true, nil
				case "other-auth":
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "different-credential"}, true, nil
				case "binding-changed":
					denied = credentialConcurrencyRPCError{code: "auth_not_found", status: 503}
				case "model-denied":
					denied = credentialConcurrencyRPCError{code: "model_not_allowed", status: 403}
				case "rpm-denied":
					denied = credentialConcurrencyRPCError{code: "rpm_exceeded", status: 429}
				case "home-enabled-during-check":
					m.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
				case "disabled-during-check":
					latest, _ := m.GetByID(auths[0].ID)
					latest.Disabled = true
					if _, errUpdate := m.Update(ctx, latest); errUpdate != nil {
						t.Fatal(errUpdate)
					}
				case "replaced-during-check":
					m.Remove(ctx, auths[0].ID)
					if _, errRegister := m.Register(ctx, &Auth{ID: auths[0].ID, Provider: "test", Status: StatusActive, Metadata: map[string]any{"concurrency_limit": 3}}); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				if denied != nil {
					return pluginapi.SchedulerPickResponse{}, true, denied
				}
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: auths[0].ID}, true, nil
			}
			m.SetPluginScheduler(scheduler)
			stream, errStream := m.ExecuteStream(ctx, []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, credentialConcurrencyOptions("synthetic-caller-scope"))
			if errStream != nil {
				t.Fatal(errStream)
			}
			awaitCredentialConcurrencySignal(t, stream.Chunks)
			if admission == nil {
				t.Fatal("missing duplex admission")
			}
			admission.ReleaseInitial()
			release, errFollowup := admission.Acquire(ctx)
			allowed := policy == "allow" || policy == "unhandled" || policy == "delegate"
			if allowed {
				if errFollowup != nil || release == nil {
					t.Fatalf("valid same-credential policy rejected: %v", errFollowup)
				}
				release()
			} else {
				if errFollowup == nil || release != nil {
					t.Fatal("changed or rejected policy admitted a follow-up")
				}
				if denied != nil && !errors.Is(errFollowup, denied) {
					t.Fatalf("plugin error was rewritten: %v, want %v", errFollowup, denied)
				}
			}
			if scheduler.calls != 2 {
				t.Fatalf("scheduler calls=%d, want initial + exactly one follow-up check", scheduler.calls)
			}
			requireCredentialConcurrencySnapshot(t, m, auths[0].ID, 0, 3)
			state, _ := m.GetByID(auths[0].ID)
			if state.Failed != 0 || state.Quota.Exceeded || len(state.ModelStates) != 0 {
				t.Fatalf("policy refusal changed provider state: %+v", state)
			}
			cancel()
			for range stream.Chunks {
			}
		})
	}
}
