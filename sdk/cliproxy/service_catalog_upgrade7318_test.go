package cliproxy

import (
	"context"
	"reflect"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestCustomCodexCatalogRegistrationPreservesMetadata7318(t *testing.T) {
	const credentialID = "custom-codex-catalog-upgrade7318"
	const apiKey = "synthetic-catalog-key7318"
	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(credentialID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(credentialID) })
	beforeOAuth := make(map[string]bool)
	for _, model := range internalregistry.GetCodexProModels() {
		beforeOAuth[model.ID] = model.SupportConfigurationUpdate
	}

	entry := config.CodexKey{
		APIKey: apiKey,
		Models: []internalconfig.CodexModel{
			{Name: "private-upstream-v1", Alias: "  Private-A  ", DisplayName: "Private display", MaxContextLength: 131072, SupportConfigurationUpdate: true, Thinking: &internalregistry.ThinkingSupport{Levels: []string{"low", "high"}}},
			{Name: "wrong-duplicate", Alias: "private-a", DisplayName: "Duplicate must not replace", MaxContextLength: 4096},
			{Name: "private-upstream-v2", Alias: "Private-B", DisplayName: "Second display", MaxContextLength: 65536},
		},
	}
	service := &Service{cfg: &config.Config{CodexKey: []config.CodexKey{entry}}}
	auth := &coreauth.Auth{
		ID: credentialID, Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: apiKey, coreauth.AttributeConfigIndex: "0", coreauth.AttributeSource: "config:codex:upgrade7318",
		},
	}

	for _, enabled := range []bool{true, false, true} {
		service.cfg.CodexKey[0].Models[0].SupportConfigurationUpdate = enabled
		service.registerModelsForAuth(context.Background(), auth)
		models := modelRegistry.GetModelsForClient(credentialID)
		if len(models) != 2 {
			t.Fatalf("custom catalog lost models or registered duplicate: count=%d, want 2", len(models))
		}
		byID := make(map[string]*internalregistry.ModelInfo, len(models))
		for _, model := range models {
			byID[model.ID] = model
		}
		first := byID["Private-A"]
		if first == nil || first.MetadataModelID != "private-upstream-v1" || first.DisplayName != "Private display" || first.ContextLength != 131072 || first.MaxContextLength != 131072 || !first.UserDefined || first.SupportConfigurationUpdate != enabled {
			t.Fatalf("first custom model metadata changed after re-registration: %#v", first)
		}
		if first.Thinking == nil || !reflect.DeepEqual(first.Thinking.Levels, []string{"low", "high"}) {
			t.Fatalf("custom thinking configuration changed: %#v", first.Thinking)
		}
		second := byID["Private-B"]
		if second == nil || second.MetadataModelID != "private-upstream-v2" || second.DisplayName != "Second display" || second.ContextLength != 65536 || second.SupportConfigurationUpdate {
			t.Fatalf("second custom model changed: %#v", second)
		}
		if first.NativeCapabilities != nil {
			t.Fatal("custom unknown upstream unexpectedly inherited OAuth native capabilities")
		}
	}

	_ = buildCodexConfigModels(&config.CodexKey{})
	afterOAuth := make(map[string]bool)
	for _, model := range internalregistry.GetCodexProModels() {
		afterOAuth[model.ID] = model.SupportConfigurationUpdate
	}
	if !reflect.DeepEqual(beforeOAuth, afterOAuth) {
		t.Fatalf("API-key catalog generation mutated OAuth capability metadata: before=%v, after=%v", beforeOAuth, afterOAuth)
	}
}
