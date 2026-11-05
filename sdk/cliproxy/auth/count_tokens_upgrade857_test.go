package auth

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestUpgrade857CountTokensUnsupportedPreservesReplacementAndReleasesLease(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	hook := &resultCaptureHook{}
	manager := NewManager(nil, nil, hook)
	const provider, model = "upgrade857-count", "upgrade857-count-model"
	oldAuth, errRegister := manager.Register(ctx, &Auth{
		ID: t.Name(), Provider: provider, Status: StatusActive, ProxyURL: "direct",
		Metadata: map[string]any{
			"access_token": "old-token", "concurrency_limit": 1,
			"codex_identity_namespace": "f655c87e-78fa-4e2d-bd4c-889ec4889600",
		},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(oldAuth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(oldAuth.ID) })
	var replacement *Auth
	countCalls := 0
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: provider,
		countFn: func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			countCalls++
			assertCredentialConcurrencyLifecycleSnapshot(t, manager, selected.ID, 1, 1)
			next := selected.Clone()
			next.ProxyURL = "http://replacement-proxy.invalid:8080"
			next.Metadata["access_token"] = "replacement-token"
			next.Metadata["codex_identity_namespace"] = "c06f9c62-75bf-4872-b8b7-0eb247a8a077"
			manager.Remove(ctx, selected.ID)
			var errReplacement error
			replacement, errReplacement = manager.Register(ctx, next)
			if errReplacement != nil {
				t.Fatal(errReplacement)
			}
			reg.RegisterClient(selected.ID, provider, []*registry.ModelInfo{{ID: model}})
			assertCredentialConcurrencyLifecycleSnapshot(t, manager, selected.ID, 1, 1)
			return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", HTTPStatus: http.StatusNotImplemented, Message: "count_tokens is not implemented"}
		},
		executeFn: func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			if selected.RegistrationEpoch != replacement.RegistrationEpoch || selected.Metadata["access_token"] != "replacement-token" {
				t.Fatal("inference used the stale credential after CountTokens completed")
			}
			assertCredentialConcurrencyLifecycleSnapshot(t, manager, selected.ID, 1, 1)
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		},
	})
	_, errCount := manager.ExecuteCount(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if statusCodeFromError(errCount) != http.StatusNotImplemented || countCalls != 1 {
		t.Fatalf("CountTokens = %v, calls = %d; want one 501 result", errCount, countCalls)
	}
	current, ok := manager.GetByID(oldAuth.ID)
	if !ok || replacement == nil || replacement.RegistrationEpoch == oldAuth.RegistrationEpoch {
		t.Fatal("expected a new registration epoch")
	}
	if current.Generation != replacement.Generation || current.Failed != replacement.Failed || current.Unavailable || len(current.ModelStates) != 0 {
		t.Fatal("the old unsupported result mutated the replacement credential")
	}
	if current.ProxyURL != replacement.ProxyURL || current.Metadata["access_token"] != "replacement-token" || current.Metadata["codex_identity_namespace"] != replacement.Metadata["codex_identity_namespace"] {
		t.Fatal("the old unsupported result changed replacement identity or proxy controls")
	}
	results := hook.Results()
	if len(results) != 1 || results[0].Success || results[0].RegistrationEpoch != oldAuth.RegistrationEpoch || results[0].Error == nil || results[0].Error.Code != "not_implemented" {
		t.Fatalf("failure audit lost the original attempt: %#v", results)
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, oldAuth.ID, 0, 1)
	if _, errExecute := manager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("inference after CountTokens 501: %v", errExecute)
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, oldAuth.ID, 0, 1)
}

func TestUpgrade857CountTokensUnsupportedHonorsRequestScopedStop(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	for _, action := range []string{"stop", "stop-and-cooldown"} {
		t.Run(action, func(t *testing.T) {
			ctx := WithSkipPersist(context.Background())
			manager := NewManager(nil, nil, nil)
			const provider, model = "upgrade857-stop", "upgrade857-stop-model"
			primary := &Auth{
				ID: t.Name(), Provider: provider, Status: StatusActive,
				Attributes: map[string]string{"priority": "10"},
				Metadata: map[string]any{
					"concurrency_limit":     1,
					"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{Status: http.StatusNotImplemented, Match: []string{"count_tokens unsupported"}, Action: action}},
				},
			}
			backup := &Auth{ID: t.Name() + "-backup", Provider: provider, Status: StatusActive}
			reg := registry.GetGlobalRegistry()
			for _, item := range []*Auth{primary, backup} {
				if _, errRegister := manager.Register(ctx, item); errRegister != nil {
					t.Fatal(errRegister)
				}
				reg.RegisterClient(item.ID, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { reg.UnregisterClient(item.ID) })
			}
			calls := 0
			manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: provider, countFn: func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls++
				if selected.ID != primary.ID {
					t.Fatal("unsupported CountTokens ignored stop and selected the backup")
				}
				return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", HTTPStatus: http.StatusNotImplemented, Message: "count_tokens unsupported"}
			}})
			_, errCount := manager.ExecuteCount(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			if statusCodeFromError(errCount) != http.StatusNotImplemented || calls != 1 {
				t.Fatalf("CountTokens = %v, calls = %d; want one 501 result", errCount, calls)
			}
			current, _ := manager.GetByID(primary.ID)
			wantCooldown := action == "stop-and-cooldown"
			if current.Unavailable != wantCooldown || current.NextRetryAfter.IsZero() == wantCooldown {
				t.Fatalf("action %q: unavailable=%t, next retry=%v", action, current.Unavailable, current.NextRetryAfter)
			}
			assertCredentialConcurrencyLifecycleSnapshot(t, manager, primary.ID, 0, 1)
		})
	}
}
