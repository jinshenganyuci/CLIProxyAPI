package pluginhost

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV837HostAuthModelsReadsFencedCatalogWithoutChangingLease(t *testing.T) {
	ctx := coreauth.WithSkipPersist(context.Background())
	manager := coreauth.NewManager(nil, nil, nil)
	meta := map[string]any{"type": "codex", "access_token": "synthetic-token-secret", "proxy_url": "direct", "concurrency_limit": 3, "excluded_models": []string{"blocked-model"}}
	if err := codex.SetCredentialIdentity(meta, "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"); err != nil {
		t.Fatal(err)
	}
	auth, err := manager.Register(ctx, &coreauth.Auth{ID: "v837-selected.json", FileName: "v837-selected.json", Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Metadata: meta})
	if err != nil {
		t.Fatal(err)
	}
	other, err := manager.Register(ctx, &coreauth.Auth{ID: "v837-other.json", FileName: "v837-other.json", Provider: "codex", Status: coreauth.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() { reg.UnregisterClient(auth.ID); reg.UnregisterClient(other.ID) })
	// The published per-credential catalog already reflects model exclusions.
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "allowed-model"}, {ID: "suspended-model"}})
	reg.RegisterClient(other.ID, "codex", []*registry.ModelInfo{{ID: "blocked-model"}})
	oldEpoch := reg.ClientRegistrationEpoch(auth.ID)
	reg.SuspendClientModel(auth.ID, "suspended-model", "synthetic")
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "fresh-model"}})
	if _, accepted := reg.ReplaceClientModels(auth.ID, "codex", oldEpoch, []*registry.ModelInfo{{ID: "stale-model"}, {ID: "blocked-model"}}); accepted {
		t.Fatal("stale catalog publication was accepted")
	}
	release, err := manager.AcquireCredentialConcurrency(ctx, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before, _ := manager.GetByID(auth.ID)
	host := New()
	host.SetAuthManager(manager)
	instance := &hostCallbackInstance{}
	callbackCtx := withHostCallbackIdentity(ctx, "synthetic-reader", instance)
	request, err := json.Marshal(pluginapi.HostAuthModelsRequest{AuthIndex: auth.Index})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := host.callFromPlugin(callbackCtx, pluginabi.MethodHostAuthModels, request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := decodeRPCEnvelope[pluginapi.HostAuthModelsResponse](raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Credentials) != 1 || response.Credentials[0].ID != auth.ID || response.Credentials[0].AuthIndex != auth.Index || !reflect.DeepEqual(response.Credentials[0].Models, []string{"fresh-model"}) {
		t.Fatal("model callback mixed credentials or resurrected an excluded/stale model")
	}
	for _, secret := range []string{"synthetic-token-secret", "proxy_url", "concurrency_limit", "codex_identity_namespace"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("catalog callback exposed credential field %s", secret)
		}
	}
	after, _ := manager.GetByID(auth.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("model callback changed credential state")
	}
	if count, limit := manager.CredentialConcurrencySnapshot(auth.ID); count != 1 || limit != 3 {
		t.Fatalf("model callback changed held lease: %d/%d", count, limit)
	}
	instance.closed.Store(true)
	if _, err := host.callFromPlugin(callbackCtx, pluginabi.MethodHostAuthModels, request); err == nil {
		t.Fatal("detached plugin callback remained usable")
	}
	release()
	if count, _ := manager.CredentialConcurrencySnapshot(auth.ID); count != 0 {
		t.Fatal("catalog read made old lease unreleasable")
	}
}
