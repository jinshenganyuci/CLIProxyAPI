package handlers

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV856AlternativeReplayProbePreservesPreparedRouteAndAdmission(t *testing.T) {
	const model = "v856-replay-probe-target"
	manager := coreauth.NewManager(nil, nil, nil)
	var executions, callbacks atomic.Int32
	manager.RegisterExecutor(&modelExecutionCaptureExecutor{
		provider: "codex",
		stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			executions.Add(1)
			return nil, nil
		},
	})
	failed := &coreauth.Auth{ID: "v856-probe-failed", Provider: "codex", Status: coreauth.StatusActive}
	alternative := &coreauth.Auth{
		ID: "v856-probe-free", Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"plan_type": "free", "concurrency_limit": "1"},
	}
	for _, auth := range []*coreauth.Auth{failed, alternative} {
		if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	var routerCalls int
	host := &handlerRouterOnlyTestHost{hasRouters: true}
	host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		routerCalls++
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "codex", TargetModel: model}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler.SetModelRouterHost(host)
	payload := []byte(`{"model":"v856-public-alias","input":[],"custom_number":9007199254740993}`)
	original := bytes.Clone(payload)
	ctx := WithPinnedAuthID(t.Context(), failed.ID)
	ctx = WithExecutionSessionID(ctx, "v856-pinned-session")
	ctx = WithSelectedAuthIDCallback(ctx, func(string) { callbacks.Add(1) })
	ctx = WithSelectedAuthProviderCallback(ctx, func(string) { callbacks.Add(1) })
	ctx = WithFinalizedResponseFormatCallback(ctx, func(string) { callbacks.Add(1) })
	ctx, routed := handler.PrepareStreamModelRoute(ctx, "openai-response", "v856-public-alias", payload)
	if !routed || routerCalls != 1 {
		t.Fatalf("prepared routing: override=%t, calls=%d", routed, routerCalls)
	}
	if !handler.HasAlternativeExecutionTarget(ctx, "openai-response", "v856-public-alias", payload, failed.ID) {
		t.Fatal("expected available alternative through the prepared provider route")
	}
	if handler.HasAlternativeExecutionTarget(WithDisallowFreeAuth(ctx), "openai-response", "v856-public-alias", payload, failed.ID) {
		t.Fatal("replay probe ignored the original free-plan restriction")
	}
	if active, limit := manager.CredentialConcurrencySnapshot(alternative.ID); active != 0 || limit != 1 {
		t.Fatalf("read-only probe consumed an admission slot: active=%d, limit=%d", active, limit)
	}
	release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), alternative)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	defer release()
	if handler.HasAlternativeExecutionTarget(ctx, "openai-response", "v856-public-alias", payload, failed.ID) {
		t.Fatal("saturated alternative must not trigger a reconnect replay")
	}
	if active, limit := manager.CredentialConcurrencySnapshot(alternative.ID); active != 1 || limit != 1 {
		t.Fatalf("probe changed the active lease: active=%d, limit=%d", active, limit)
	}
	if routerCalls != 1 || executions.Load() != 0 || callbacks.Load() != 0 || !bytes.Equal(payload, original) {
		t.Fatalf("probe changed execution state: routes=%d, executes=%d, callbacks=%d, payloadChanged=%t", routerCalls, executions.Load(), callbacks.Load(), !bytes.Equal(payload, original))
	}
	if pinnedAuthIDFromContext(ctx) != failed.ID || executionSessionIDFromContext(ctx) != "v856-pinned-session" {
		t.Fatal("probe removed the live turn's affinity")
	}
}
