package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV823UIPluginQuotaListAndFetchUseStablePluginRoutes(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "synthetic-quota-auth", Provider: "synthetic-provider", ProxyURL: "direct", Metadata: map[string]any{"concurrency_limit": 3}}
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(t.Context()), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	index := auth.EnsureIndex()
	fetches := 0
	host := pluginhost.New()
	host.SetAuthManager(manager)
	host.RegisterPluginForTest("synthetic-quota-plugin", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		QuotaProvider: &mockQuotaProvider{identifier: auth.Provider, fetchFn: func(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			fetches++
			if req.AuthID != auth.ID || req.AuthIndex != index || req.Provider != auth.Provider {
				t.Error("plugin quota fetch selected a different credential")
			}
			return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "synthetic-plan"}}, nil
		}},
	}})
	host.RegisterPluginForTest("synthetic-probe-only", pluginapi.Plugin{})
	t.Cleanup(host.ShutdownAll)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), Plugins: config.PluginsConfig{Enabled: true, Dir: t.TempDir()}}, manager)
	h.SetPluginHost(host)
	router := gin.New()
	router.GET("/v8/management/plugins", h.ListPlugins)
	router.POST("/v8/management/plugins/:id/quota", h.FetchPluginQuota)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v8/management/plugins", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("plugin list status=%d: %s", recorder.Code, recorder.Body.String())
	}
	var listed pluginListResponse
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &listed); errDecode != nil {
		t.Fatal(errDecode)
	}
	matched := ""
	for _, plugin := range listed.Plugins {
		if plugin.ID == "synthetic-probe-only" && (plugin.SupportsQuota || plugin.QuotaProvider != "") {
			t.Fatal("plugin without QuotaProvider advertised generic quota support")
		}
		if plugin.SupportsQuota && plugin.QuotaProvider == auth.Provider {
			if matched != "" {
				t.Fatal("fixture did not provide a unique quota provider")
			}
			matched = plugin.ID
		}
	}
	if matched != "synthetic-quota-plugin" {
		t.Fatal("stable plugin list omitted the metadata required by UI 1.26.1")
	}
	for _, tc := range []struct {
		id, body string
		status   int
	}{
		{matched, `{"auth_index":"` + index + `"}`, http.StatusOK},
		{"synthetic-probe-only", `{"auth_index":"` + index + `"}`, http.StatusNotFound},
		{matched, `{"auth_index":"missing"}`, http.StatusNotFound},
		{matched, `{}`, http.StatusBadRequest},
	} {
		recorder = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v8/management/plugins/"+tc.id+"/quota", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		if recorder.Code != tc.status {
			t.Fatalf("plugin %s status=%d, want %d: %s", tc.id, recorder.Code, tc.status, recorder.Body.String())
		}
	}
	if fetches != 1 {
		t.Fatalf("invalid plugin/credential requests triggered %d quota fetches", fetches)
	}
	if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 0 || limit != 3 {
		t.Fatalf("quota lookup changed model request concurrency: %d/%d", active, limit)
	}
	stored, _ := manager.GetByID(auth.ID)
	if stored.ProxyURL != "direct" || stored.Disabled || stored.Unavailable || stored.Quota.Exceeded {
		t.Fatal("quota lookup changed credential routing or availability")
	}
}
