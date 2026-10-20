package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestV835PluginGenerationConflictReconcilesWithoutReplacingCredentials(t *testing.T) {
	ctx := coreauth.WithSkipPersist(context.Background())
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager, pluginHost: pluginhost.New()}
	auth, err := manager.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Metadata: map[string]any{"access_token": "old", "codex_identity_namespace": "stable", "concurrency_limit": 3}})
	if err != nil {
		t.Fatal(err)
	}
	reg := GlobalModelRegistry()
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	calls := 0
	previous := pluginModelsForAuth
	pluginModelsForAuth = func(_ context.Context, _ *pluginhost.Host, snapshot *coreauth.Auth) pluginhost.AuthModelResult {
		calls++
		if calls == 1 {
			current, _ := manager.GetByID(auth.ID)
			current.Metadata["access_token"] = "fresh"
			current.Metadata["concurrency_limit"] = 2
			current.ProxyURL = "http://new.invalid:8080"
			if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			stale := snapshot.Clone()
			stale.Metadata["access_token"] = "stale-plugin-token"
			return pluginhost.AuthModelResult{Handled: true, Provider: "codex", Auth: stale, Models: []*ModelInfo{{ID: "stale-model"}}}
		}
		return pluginhost.AuthModelResult{Handled: true, Provider: "codex", Models: []*ModelInfo{{ID: "current-model"}}}
	}
	t.Cleanup(func() { pluginModelsForAuth = previous })
	service.completeModelRegistrationForAuth(ctx, auth)
	current, _ := manager.GetByID(auth.ID)
	if calls != 2 || current.Metadata["access_token"] != "fresh" || current.ProxyURL != "http://new.invalid:8080" || current.Metadata["codex_identity_namespace"] != "stable" || coreauth.EffectiveConcurrencyLimit(current) != 2 {
		t.Fatalf("reconciliation lost current credential: calls=%d auth=%+v", calls, current)
	}
	if !reg.ClientSupportsModel(auth.ID, "current-model") || reg.ClientSupportsModel(auth.ID, "stale-model") {
		t.Fatal("generation conflict did not reconcile latest models")
	}
}
