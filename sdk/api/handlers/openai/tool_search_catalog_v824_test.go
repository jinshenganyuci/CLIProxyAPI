package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestV824PublicCodexCatalogRequiresToolSearchOnEveryServingCredential(t *testing.T) {
	originalSwitch := responsesclaude.ClientToolSearchEnabled()
	t.Cleanup(func() { responsesclaude.SetClientToolSearchEnabled(originalSwitch) })
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "synthetic-official", Models: []config.ClaudeModel{{Name: "claude-opus-4", Alias: "v824-legacy-alias"}}}}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
	for _, entry := range []struct {
		id, provider, key, base string
		optIn, disabled         bool
		models                  []string
	}{
		{id: "official", provider: "claude", key: "synthetic-official", models: []string{"v824-official", "v824-mixed", "v824-disabled-mixed", "v824-mixed-provider", "v824-legacy-alias", "team/v824-exact"}},
		{id: "custom", provider: "claude", key: "synthetic-custom", base: "https://synthetic.invalid", models: []string{"v824-mixed", "v824-exact"}},
		{id: "opt-in", provider: "claude", key: "synthetic-opt-in", base: "https://synthetic.invalid", optIn: true, models: []string{"v824-opt-in"}},
		{id: "disabled", provider: "claude", key: "synthetic-disabled", base: "https://synthetic.invalid", disabled: true, models: []string{"v824-disabled-mixed"}},
		{id: "other-provider", provider: "openai-compatibility", key: "synthetic-other", models: []string{"v824-mixed-provider", "v824-unknown"}},
	} {
		id := t.Name() + "-" + entry.id
		attrs := map[string]string{"api_key": entry.key, "base_url": entry.base}
		if entry.optIn {
			attrs["native_tool_search"] = "true"
		}
		_, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: entry.provider, Status: coreauth.StatusActive, Disabled: entry.disabled, Attributes: attrs})
		if err != nil {
			t.Fatal(err)
		}
		infos := make([]*registry.ModelInfo, 0, len(entry.models))
		for _, model := range entry.models {
			infos = append(infos, &registry.ModelInfo{ID: model})
		}
		registry.GetGlobalRegistry().RegisterClient(id, entry.provider, infos)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	for _, enabled := range []bool{true, false} {
		responsesclaude.SetClientToolSearchEnabled(enabled)
		writer := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(writer)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.153.4", nil)
		handler.OpenAIModels(c)
		if writer.Code != 200 {
			t.Fatalf("catalog failed: %d %s", writer.Code, writer.Body.String())
		}
		actual := map[string]bool{}
		for _, model := range gjson.GetBytes(writer.Body.Bytes(), "models").Array() {
			actual[model.Get("slug").String()] = model.Get("supports_search_tool").Bool()
		}
		for model, supported := range map[string]bool{"v824-official": true, "v824-mixed": false, "v824-opt-in": true, "v824-disabled-mixed": true, "v824-mixed-provider": false, "v824-unknown": false, "v824-legacy-alias": false, "team/v824-exact": true} {
			got, exists := actual[model]
			if !exists || got != (enabled && supported) {
				t.Errorf("tool-search=%t model=%s support=%t exists=%t, want %t", enabled, model, got, exists, enabled && supported)
			}
		}
	}
}
