package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

// Captured from the actual Home buildModelsJSON at aadf006b487e6a58a28207983d73a2777d52643f.
//
//go:embed testdata/home_tool_search_v824.json
var homeToolSearchV824Fixture []byte

func TestV824CurrentHomeProducerToolSearchCatalogOverRESP(t *testing.T) {
	for _, mode := range []string{"current-home", "old-home", "mixed-provider"} {
		t.Run(mode, func(t *testing.T) {
			var wire map[string][]map[string]any
			if errDecode := json.Unmarshal(homeToolSearchV824Fixture, &wire); errDecode != nil {
				t.Fatal(errDecode)
			}
			if mode == "old-home" {
				for _, model := range wire["claude"] {
					delete(model["native_capabilities"].(map[string]any), "tool_search")
				}
			}
			if mode == "mixed-provider" {
				wire["unrecognized-route"] = []map[string]any{{"id": "m-official"}, {"id": "m-forced"}}
			}
			payload, errEncode := json.Marshal(wire)
			if errEncode != nil {
				t.Fatal(errEncode)
			}
			server := newTestServer(t)
			server.cfg.Home.Enabled = true
			previousHome := home.Current()
			home.SetCurrent(newHomeCatalogClient(t, string(payload)))
			t.Cleanup(func() { home.SetCurrent(previousHome) })
			engine := gin.New()
			engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
			for _, enabled := range []bool{false, true, false} {
				setClaudeToolSearchForTest(t, enabled)
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.153.4", nil))
				if recorder.Code != http.StatusOK {
					t.Fatalf("catalog status=%d: %s", recorder.Code, recorder.Body.String())
				}
				var response struct {
					Models []map[string]any `json:"models"`
				}
				if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
					t.Fatal(errDecode)
				}
				if len(response.Models) != 5 {
					t.Fatalf("producer catalog has %d models, want 5", len(response.Models))
				}
				for _, model := range response.Models {
					id, _ := model["slug"].(string)
					want := enabled && mode == "current-home" && (id == "m-official" || id == "m-forced")
					if got, _ := model["supports_search_tool"].(bool); got != want {
						t.Errorf("enabled=%v model=%s search=%v, want %v", enabled, id, got, want)
					}
					if model["apply_patch_tool_type"] != nil {
						t.Error("tool-search catalog widened the independent apply-patch capability")
					}
					if _, present := model["native_capabilities"]; present {
						t.Error("Home-only capability metadata escaped into the client catalog")
					}
				}
			}
		})
	}
}
