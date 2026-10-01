package cliproxy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestOAuthSettingsV808RegistrationKeepsCredentialAndCatalogIsolation(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const modelID = "gpt-6.1-sol"
	findModel := func(models []*registry.ModelInfo, id string) *registry.ModelInfo {
		t.Helper()
		for _, model := range models {
			if model.ID == id {
				return model
			}
		}
		t.Fatalf("model %s not registered", id)
		return nil
	}
	native := findModel(registry.GetCodexProModels(), modelID)
	nativeContext, nativeMaximum := native.ContextLength, native.MaxContextLength
	limit := 3
	cfg := &config.Config{
		CodexKey:        []config.CodexKey{{APIKey: "synthetic-v808-key", BaseURL: "https://example.invalid/v1", ConcurrencyLimit: &limit, Models: []config.CodexModel{{Name: modelID, Alias: "public", MaxContextLength: 131072}}}},
		OAuthModelAlias: map[string][]config.OAuthModelAlias{"codex": {{Name: modelID, Alias: "public"}}},
		OAuthSettings: map[string][]config.OAuthModelSetting{"codex": {
			{Name: modelID, MaxContextLength: 524288},
			{Name: modelID, Alias: "public", MaxContextLength: 1048576},
		}},
	}
	service := &Service{cfg: cfg}
	oauth := &coreauth.Auth{ID: "oauth-settings-v808-oauth", Provider: "codex", Prefix: "bound", Status: coreauth.StatusActive, ProxyURL: "socks5h://127.0.0.1:1080",
		Attributes: map[string]string{"plan_type": "pro"}, Metadata: map[string]any{"type": "codex", "concurrency_limit": 3, codex.CredentialIdentityVersionMetadataKey: 1, codex.CredentialIdentityNamespaceMetadataKey: "94c2f1b0-1af0-4bf1-9d2b-c4c33b299119"}}
	apiKey := &coreauth.Auth{ID: "oauth-settings-v808-key", Provider: "codex", Prefix: "bound", Status: coreauth.StatusActive, ProxyURL: "direct",
		Attributes: map[string]string{coreauth.AttributeAPIKey: "synthetic-v808-key", "base_url": "https://example.invalid/v1", coreauth.AttributeConfigIndex: "0", coreauth.AttributeSource: "config:codex:v808"}}
	before, errMarshal := json.Marshal([]*coreauth.Auth{oauth, apiKey})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for _, auth := range []*coreauth.Auth{oauth, apiKey} {
		t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
		service.registerModelsForAuth(context.Background(), auth)
	}
	assertContext := func(authID string, want int) {
		t.Helper()
		model := findModel(modelRegistry.GetModelsForClient(authID), "bound/public")
		if model.ContextLength != want || model.MaxContextLength != want {
			t.Fatalf("%s context=%d/%d, want %d", authID, model.ContextLength, model.MaxContextLength, want)
		}
	}
	assertContext(oauth.ID, 1048576)
	assertContext(apiKey.ID, 131072)
	// Re-registering after an OAuth settings update must not contaminate API keys
	// or mutate the static catalog shared by other credentials.
	cfg.OAuthSettings["codex"][1].MaxContextLength = 786432
	service.registerModelsForAuth(context.Background(), oauth)
	assertContext(oauth.ID, 786432)
	assertContext(apiKey.ID, 131072)
	currentNative := findModel(registry.GetCodexProModels(), modelID)
	if currentNative.ContextLength != nativeContext || currentNative.MaxContextLength != nativeMaximum {
		t.Fatal("OAuth override mutated the shared native model catalog")
	}
	after, errMarshal := json.Marshal([]*coreauth.Auth{oauth, apiKey})
	if errMarshal != nil || string(before) != string(after) {
		t.Fatalf("model registration changed credential proxy, identity, or admission metadata: %v", errMarshal)
	}
}
