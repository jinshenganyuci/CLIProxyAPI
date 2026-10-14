package cliproxy

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestV828BatchConfigSnapshotPreservesThreeCredentialControls(t *testing.T) {
	ctx := context.Background()
	const provider = "compat-snapshot-v828"
	makeConfig := func(alias string, force bool) *config.Config {
		return &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: force}, OpenAICompatibility: []config.OpenAICompatibility{{Name: provider, Models: []config.OpenAICompatibilityModel{{Name: "upstream-model", Alias: alias}}}}}
	}
	initial := makeConfig("old-alias", false)
	next := makeConfig("new-alias", true)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: provider})
	service := &Service{cfg: initial, coreManager: manager}
	var auths []*coreauth.Auth
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i, limit := range []int{3, 2, 10} {
		proxy := []string{"direct", "http://selected.invalid:8123", "socks5://selected.invalid:8124"}[i]
		a := &coreauth.Auth{ID: fmt.Sprintf("v828-snapshot-%d", i), Provider: provider, Prefix: fmt.Sprintf("p%d", i), Status: coreauth.StatusActive, ProxyURL: proxy,
			Attributes: map[string]string{"auth_kind": "apikey", "compat_name": provider, "provider_key": provider, "custom_plugin_field": "retained"},
			Metadata:   map[string]any{"proxy_url": proxy, "codex_identity_namespace": fmt.Sprintf("identity-%d", i), "access_token": "synthetic"}}
		if limit != 10 {
			a.Metadata["concurrency_limit"] = limit
		}
		live, err := manager.Register(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		auths = append(auths, live)
		t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(live.ID) })
		release, err := manager.AcquireCredentialConcurrency(ctx, live)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	service.registerModelsForAuthBatch(ctx, auths)
	var once sync.Once
	previousHook := modelRegistrationTaskHook
	modelRegistrationTaskHook = func() { once.Do(func() { service.commitConfigUpdate(next) }) }
	defer func() { modelRegistrationTaskHook = previousHook }()
	service.registerModelsForAuthBatch(ctx, auths)
	for i, before := range auths {
		model := fmt.Sprintf("p%d/new-alias", i)
		if !GlobalModelRegistry().ClientSupportsModel(before.ID, model) || GlobalModelRegistry().ClientSupportsModel(before.ID, fmt.Sprintf("p%d/old-alias", i)) || GlobalModelRegistry().ClientSupportsModel(before.ID, "new-alias") {
			t.Fatalf("credential %s mixed old model cache with the new prefix policy", before.ID)
		}
		got, ok := manager.GetByID(before.ID)
		if !ok || got.ProxyURL != before.ProxyURL || got.Index != before.Index || got.RegistrationEpoch != before.RegistrationEpoch || got.CredentialVersion != before.CredentialVersion || !reflect.DeepEqual(got.Metadata, before.Metadata) || !reflect.DeepEqual(got.Attributes, before.Attributes) {
			t.Fatal("model registration changed credential controls, identity, or plugin fields")
		}
		limit := []int{3, 2, 10}[i]
		if n, cap := manager.CredentialConcurrencySnapshot(got.ID); n != 1 || cap != limit {
			t.Fatalf("occupied credential changed: %d/%d", n, cap)
		}
	}
	for _, release := range releases {
		release()
	}
	for _, a := range auths {
		if n, _ := manager.CredentialConcurrencySnapshot(a.ID); n != 0 {
			t.Fatal("model registration detached a live lease")
		}
	}
}
