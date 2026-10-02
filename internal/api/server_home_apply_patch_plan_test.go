package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Captured from Home's real buildModelsJSON at e16d6f1092156af448071add89a0675e431c95a1.
const homePlanCatalogFixture = `{"codex-free":[{"id":"home-native-free","object":"","created":0,"owned_by":"","type":""}],"codex-plus":[{"id":"home-native-plus","object":"","created":0,"owned_by":"","type":""}],"codex-pro":[{"id":"home-native-pro","object":"","created":0,"owned_by":"","type":""}],"codex-team":[{"id":"home-native-team","object":"","created":0,"owned_by":"","type":""}]}`

func TestHomeApplyPatchKnownPlanBuckets(t *testing.T) {
	for _, tc := range []struct {
		name, extraProvider string
		wantFree            bool
	}{
		{name: "real-producer", wantFree: true},
		{name: "mixed-supported", extraProvider: "custom", wantFree: true},
		{name: "mixed-unsupported", extraProvider: "unsupported"},
		{name: "mixed-unregistered", extraProvider: "missing"},
		{name: "unknown-codex-prefix", extraProvider: "codex-business"},
		{name: "blank-route", extraProvider: " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire map[string]any
			if errDecode := json.Unmarshal([]byte(homePlanCatalogFixture), &wire); errDecode != nil {
				t.Fatal(errDecode)
			}
			if tc.extraProvider != "" {
				wire[tc.extraProvider] = []map[string]any{{"id": "home-native-free"}}
			}
			raw, errEncode := json.Marshal(wire)
			if errEncode != nil {
				t.Fatal(errEncode)
			}
			entries, errDecode := decodeHomeModels(raw)
			if errDecode != nil {
				t.Fatal(errDecode)
			}
			providersBefore := make(map[string][]string)
			for _, entry := range entries {
				providersBefore[entry.id] = append([]string(nil), entry.providers...)
			}
			manager := auth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor.NewCodexAutoExecutor(&config.Config{}))
			manager.RegisterExecutor(executor.NewOpenAICompatExecutor("custom", &config.Config{}))
			manager.RegisterExecutor(modelCatalogUnsupportedExecutor{executor.NewOpenAICompatExecutor("unsupported", &config.Config{})})
			lookup := homeApplyPatchCapabilityForModel(entries, manager)
			for _, plan := range []string{"free", "plus", "team", "pro"} {
				id := "home-native-" + plan
				want := plan != "free" || tc.wantFree
				if got := lookup(id); got != want {
					t.Errorf("%s support = %t, want %t", id, got, want)
				}
				if homeApplyPatchCapabilityForModel(entries, nil)(id) {
					t.Errorf("%s supported without an executor manager", id)
				}
				if lookup("team/"+id) || lookup(id+"(high)") {
					t.Errorf("%s inferred a non-exact public route", id)
				}
				if got := homeWebSearchCapabilityForModel(entries)(id); got != nil {
					t.Errorf("apply_patch normalization widened independent web search policy for %s", id)
				}
			}
			for _, entry := range entries {
				if !reflect.DeepEqual(entry.providers, providersBefore[entry.id]) {
					t.Fatalf("capability lookup mutated actual routing providers for %s", entry.id)
				}
			}
		})
	}
}

func TestHomeApplyPatchPlanCatalogOverRESP(t *testing.T) {
	server := newTestServer(t)
	server.cfg.Home.Enabled = true
	server.handlers.AuthManager.RegisterExecutor(executor.NewCodexAutoExecutor(&config.Config{}))
	engine := gin.New()
	engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
	previousHome := home.Current()
	home.SetCurrent(newHomeCatalogClient(t, homePlanCatalogFixture))
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	for _, enabled := range []bool{false, true, false} {
		server.cfg.Client.Codex.EnableApplyPatch = enabled
		for _, version := range []string{"0.137.0", "0.153.4", "cpa"} {
			request := httptest.NewRequest(http.MethodGet, "/v1/models?client_version="+version, nil)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Models []map[string]any `json:"models"`
			}
			if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
				t.Fatal(errDecode)
			}
			if len(response.Models) != 4 {
				t.Fatalf("catalog has %d models, want the four producer plan models", len(response.Models))
			}
			for _, entry := range response.Models {
				var want any
				if enabled {
					want = "freeform"
				}
				if got, exists := entry["apply_patch_tool_type"]; !exists || got != want {
					t.Errorf("enabled=%t client=%s model=%s patch type=%v, want %v", enabled, version, entry["slug"], got, want)
				}
			}
		}
	}
}
