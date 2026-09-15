package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type pinnedTransportFailureExecutor struct {
	transportThenSuccessExecutor
	selectedMu sync.Mutex
	selected   []*Auth
}

func (e *pinnedTransportFailureExecutor) recordAuth(auth *Auth) error {
	e.selectedMu.Lock()
	e.selected = append(e.selected, auth.Clone())
	e.selectedMu.Unlock()
	return dialRefusedError()
}

func (e *pinnedTransportFailureExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.recordAuth(auth)
}

func (e *pinnedTransportFailureExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, e.recordAuth(auth)
}

func (e *pinnedTransportFailureExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.recordAuth(auth)
}

func TestPinnedTransportRetriesRetainCredentialProxyAndIdentity(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "count"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetRetryConfig(2, 0, 0)
			executor := &pinnedTransportFailureExecutor{transportThenSuccessExecutor: transportThenSuccessExecutor{identifier: "codex"}}
			manager.RegisterExecutor(executor)
			model, selectedID := "transport-pinned-"+mode, "transport-selected-"+mode
			const proxyURL = "socks5://selected.example.test:1080"
			const namespace = "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"
			for _, auth := range []*Auth{
				{ID: selectedID, Provider: "codex", ProxyURL: proxyURL, Metadata: map[string]any{"codex_identity_namespace": namespace}},
				{ID: "transport-other-" + mode, Provider: "codex", ProxyURL: "direct"},
			} {
				if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}
			manager.SetPluginScheduler(&fakePluginScheduler{pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
				if len(req.Candidates) != 1 || req.Candidates[0].ID != selectedID {
					return pluginapi.SchedulerPickResponse{}, false, fmt.Errorf("plugin received unpinned candidates")
				}
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: selectedID}, true, nil
			}})
			req := cliproxyexecutor.Request{Model: model}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: selectedID}}
			var errExecute error
			switch mode {
			case "execute":
				_, errExecute = manager.Execute(context.Background(), []string{"codex"}, req, opts)
			case "stream":
				_, errExecute = manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
			case "count":
				_, errExecute = manager.ExecuteCount(context.Background(), []string{"codex"}, req, opts)
			}
			if errExecute == nil || !isTransientTransportError(errExecute) {
				t.Fatalf("terminal error = %v, want transport failure after bounded retries", errExecute)
			}
			executor.selectedMu.Lock()
			defer executor.selectedMu.Unlock()
			if len(executor.selected) != 3 {
				t.Fatalf("attempts = %d, want initial plus two retries", len(executor.selected))
			}
			for _, auth := range executor.selected {
				if auth.ID != selectedID || auth.ProxyURL != proxyURL || auth.Metadata["codex_identity_namespace"] != namespace {
					t.Fatalf("retry changed selected credential/proxy/identity: id=%s", auth.ID)
				}
			}
			assertNoCooldown(t, manager, selectedID, model)
		})
	}
}
