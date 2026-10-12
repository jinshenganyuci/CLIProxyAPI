package cliproxy

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type serviceCodexPluginV824 struct{ serviceTestPluginExecutor }

func (*serviceCodexPluginV824) Identifier() string { return "codex" }

func TestV824FirstCodexAuthAfterEmptyReloadUsesCurrentConfigAndKeepsLeases(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("steering=%t", enabled), func(t *testing.T) {
			ctx := context.Background()
			initial := &config.Config{}
			initial.Codex.ResponseSteering = !enabled
			manager := coreauth.NewManager(nil, nil, nil)
			service := &Service{cfg: initial, coreManager: manager}
			service.registerAvailableExecutors(ctx, executorRegistrationOptions{includeBaseline: true})
			old, _ := manager.Executor("codex")
			current := &config.Config{}
			current.Codex.ResponseSteering = enabled
			commit := service.commitConfigUpdate(current)
			if !service.applyConfigRuntime(ctx, commit, false) {
				t.Fatal("empty-auth runtime reload failed")
			}
			if len(manager.List()) != 0 {
				t.Fatal("runtime reload synthesized an unexpected credential")
			}
			if got, _ := manager.Executor("codex"); got != old {
				t.Fatal("empty-auth reload changed baseline registration policy")
			}
			var selected coreauth.ProviderExecutor
			var releases []func()
			defer func() {
				for _, release := range releases {
					release()
				}
			}()
			for i, limit := range []int{3, 2, 10} {
				id := fmt.Sprintf("%s-%d", t.Name(), i)
				auth := &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Metadata: map[string]any{"access_token": "synthetic-token", "codex_identity_namespace": "fixed-namespace"}}
				if limit != 10 {
					auth.Metadata["concurrency_limit"] = limit
				}
				t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(id) })
				service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: id, Auth: auth})
				got, _ := manager.Executor("codex")
				actual, ok := got.(*runtimeexecutor.CodexAutoExecutor)
				if !ok || !actual.UsesConfig(current) {
					t.Fatalf("first credential retained stale executor config: %T", got)
				}
				if i == 0 {
					if got == old {
						t.Fatal("first credential did not replace stale executor")
					}
					selected = got
				} else if got != selected {
					t.Fatal("same-config credential registration replaced live executor")
				}
				live, ok := manager.GetByID(id)
				if !ok {
					t.Fatal("credential did not register")
				}
				if live.ProxyURL != "direct" || live.Metadata["codex_identity_namespace"] != "fixed-namespace" || live.CredentialVersion != 1 {
					t.Fatal("registration changed credential controls")
				}
				release, err := manager.AcquireCredentialConcurrency(ctx, live)
				if err != nil {
					t.Fatal(err)
				}
				releases = append(releases, release)
				service.ensureExecutorsForAuth(live)
				if after, _ := manager.Executor("codex"); after != selected {
					t.Fatal("same-config update replaced occupied executor")
				}
				if n, cap := manager.CredentialConcurrencySnapshot(id); n != 1 || cap != limit {
					t.Fatalf("lease changed: %d/%d, want 1/%d", n, cap, limit)
				}
			}
			if !old.(*runtimeexecutor.CodexAutoExecutor).UsesConfig(initial) {
				t.Fatal("replacement mutated the previous config snapshot")
			}
			for _, release := range releases {
				release()
			}
			for _, a := range manager.List() {
				if n, _ := manager.CredentialConcurrencySnapshot(a.ID); n != 0 {
					t.Fatal("lease did not release")
				}
			}
		})
	}
}

func TestV824EmptyCodexReloadPreservesPluginExecutorPrecedence(t *testing.T) {
	ctx := context.Background()
	initial := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: initial, coreManager: manager, pluginHost: pluginhost.New()}
	plugin := &serviceCodexPluginV824{}
	previous := registerPluginExecutors
	registerPluginExecutors = func(_ *pluginhost.Host, m *coreauth.Manager) { m.RegisterExecutor(plugin) }
	t.Cleanup(func() { registerPluginExecutors = previous })
	service.registerAvailableExecutors(ctx, executorRegistrationOptions{includeBaseline: true, includePlugins: true})
	if got, _ := manager.Executor("codex"); got != plugin {
		t.Fatal("startup baseline displaced plugin")
	}
	next := &config.Config{}
	next.Codex.ResponseSteering = true
	if !service.applyConfigRuntime(ctx, service.commitConfigUpdate(next), false) {
		t.Fatal("empty config reload failed")
	}
	if got, _ := manager.Executor("codex"); got != plugin {
		t.Fatal("empty reload displaced plugin executor")
	}
	service.registerAvailableExecutors(ctx, executorRegistrationOptions{includeBaseline: true, includePlugins: true, forceReplaceAuths: true, auths: []*coreauth.Auth{{ID: "first-plugin-codex", Provider: "codex"}}})
	if got, _ := manager.Executor("codex"); got != plugin {
		t.Fatal("auth registration changed plugin-last precedence")
	}
}
