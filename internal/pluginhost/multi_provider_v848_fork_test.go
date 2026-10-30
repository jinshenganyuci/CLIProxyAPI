package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV848MultiProviderCatalogKeepsNativePriorityAndCredentialPublication(t *testing.T) {
	host := New()
	t.Cleanup(host.ShutdownAll)
	models := registry.GetGlobalRegistry()
	manager := newFakeExecutorManager()
	native := &fakeProviderExecutor{provider: "v848-native"}
	manager.RegisterExecutor(native)
	host.RegisterPluginForTest("v848-catalog", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		ModelRegistrar: modelRegistrarFunc(func(context.Context, pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
			return pluginapi.ModelRegistrationResponse{Registrations: []pluginapi.ProviderModels{
				{Provider: "v848-native", Models: []pluginapi.ModelInfo{{ID: "v848-reserved"}}},
				{Provider: "v848-second", Models: []pluginapi.ModelInfo{{ID: "v848-second-model"}}},
			}}, nil
		}),
	}})
	host.RegisterPluginForTest("v848-competing-executor", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		ModelRegistrar: staticModelRegistrar("v848-competitor", "v848-reserved"),
		Executor:       &fakeExecutor{identifier: "v848-competitor"},
	}})
	host.RegisterModels(context.Background(), models)
	host.RegisterExecutors(manager, models)
	if got, _ := manager.Executor("v848-native"); got != native {
		t.Fatal("catalog replaced native executor")
	}
	if _, exists := manager.Executor("v848-competitor"); exists {
		t.Fatal("plugin executor stole model reserved by native catalog")
	}
	for _, id := range []string{"v848-reserved", "v848-second-model"} {
		if got := models.GetModelProviders(id); len(got) != 0 {
			t.Fatalf("catalog-only advertised %s without credentials: %v", id, got)
		}
	}
	models.RegisterClient("v848-credential", "v848-second", host.ModelsForProvider("v848-second"))
	t.Cleanup(func() { models.UnregisterClient("v848-credential") })
	if providers := models.GetModelProviders("v848-second-model"); len(providers) != 1 || providers[0] != "v848-second" {
		t.Fatalf("second catalog provider cannot be credential-published: %v", providers)
	}
	if !host.UnloadPlugin("v848-catalog") {
		t.Fatal("catalog unload failed")
	}
	if len(host.ModelsForProvider("v848-native")) != 0 || len(host.ModelsForProvider("v848-second")) != 0 {
		t.Fatal("unload retained one catalog provider")
	}
	if got := models.GetModelsForClient("v848-credential"); len(got) != 1 || got[0].ID != "v848-second-model" {
		t.Fatal("unloading catalog deleted independently owned credential registration")
	}
}
