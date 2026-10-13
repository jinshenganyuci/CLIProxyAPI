package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV827CredentialQuotaPluginIDRoutesExactCredential(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	before := make(map[string]*coreauth.Auth)
	for ordinal, entry := range []struct {
		id, provider, proxy string
		limit               any
	}{
		{"v827-foo-a", "foo", "direct", 3},
		{"v827-foo-b", "foo", "socks5h://127.0.0.1:1080", nil},
		{"v827-bar", "bar", "http://127.0.0.1:8080", 5},
	} {
		record := &coreauth.Auth{
			ID: entry.id, FileName: "shared.json", Provider: entry.provider, ProxyURL: entry.proxy,
			Attributes: map[string]string{"runtime_only": "true"},
			Metadata:   map[string]any{"concurrency_limit": entry.limit, "proxy_url": entry.proxy, "plugin_owned": false},
		}
		coreauth.MarkPluginVirtualAuth(record, "synthetic-v827/shared.json", ordinal)
		auth, errRegister := manager.Register(t.Context(), record)
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		before[auth.ID] = auth.Clone()
		release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), auth)
		if errAcquire != nil {
			t.Fatal(errAcquire)
		}
		t.Cleanup(release)
	}
	host := pluginhost.New()
	host.SetAuthManager(manager)
	t.Cleanup(host.ShutdownAll)
	fetches := make(map[string]int)
	for _, item := range []struct{ id, provider string }{{"a-multi-quota", "multi"}, {"z-foo-quota", "foo"}} {
		pluginID := item.id
		quota := &mockQuotaProvider{identifier: item.provider, supported: []string{"foo", "bar"}}
		quota.fetchFn = func(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			previous := before[req.AuthID]
			if previous == nil || previous.EnsureIndex() != req.AuthIndex || previous.Provider != req.Provider || req.Metadata["plugin_owned"] != false {
				t.Error("quota request selected the wrong credential or changed opaque metadata")
			}
			wantPlugin := "a-multi-quota"
			if req.Provider == "foo" {
				wantPlugin = "z-foo-quota"
			}
			if pluginID != wantPlugin {
				t.Errorf("quota fetch used %s, want selected %s", pluginID, wantPlugin)
			}
			fetches[req.AuthID]++
			return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "synthetic"}}, nil
		}
		host.RegisterPluginForTest(item.id, pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: quota}})
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetPluginHost(host)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(ConfigV8ContextKey, true); c.Next() })
	router.GET("/v8/management/credentials", h.ListAuthFiles)
	router.POST("/v8/management/plugins/:id/quota", h.FetchPluginQuota)
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d", list.Code)
	}
	var payload struct {
		Files []struct {
			ID          string `json:"id"`
			AuthIndex   string `json:"auth_index"`
			PluginID    string `json:"quota_plugin_id"`
			Supports    bool   `json:"supports_quota"`
			Concurrency int    `json:"concurrency_limit"`
			Active      int    `json:"concurrency_in_flight"`
		} `json:"files"`
	}
	if errDecode := json.Unmarshal(list.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(payload.Files) != 3 {
		t.Fatalf("listed %d credentials, want 3", len(payload.Files))
	}
	post := func(method, route string, body []byte) {
		t.Helper()
		if !strings.HasPrefix(route, "/v8/management/") {
			route = "/v8/management" + route
		}
		req := httptest.NewRequest(method, route, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("quota POST returned %d: %s", response.Code, response.Body.String())
		}
	}
	for _, file := range payload.Files {
		wantID, wantLimit := "z-foo-quota", 3
		if file.ID == "v827-foo-b" {
			wantLimit = 10
		} else if file.ID == "v827-bar" {
			wantID, wantLimit = "a-multi-quota", 5
		}
		if !file.Supports || file.PluginID != wantID || file.AuthIndex == "" || file.Active != 1 || file.Concurrency != wantLimit {
			t.Fatalf("credential %s lost quota routing or occupancy", file.ID)
		}
		body, _ := json.Marshal(map[string]string{"auth_index": file.AuthIndex})
		post(http.MethodPost, "/plugins/"+file.PluginID+"/quota", body)
	}
	if output := os.Getenv("CPA_V827_QUOTA_FIXTURE"); output != "" {
		if errWrite := os.WriteFile(output, list.Body.Bytes(), 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	if input := os.Getenv("CPA_V827_QUOTA_UI_REQUESTS"); input != "" {
		raw, errRead := os.ReadFile(input)
		if errRead != nil {
			t.Fatal(errRead)
		}
		var replay struct {
			Requests []struct {
				Method string          `json:"method"`
				URL    string          `json:"url"`
				Body   json.RawMessage `json:"body"`
			} `json:"requests"`
		}
		if errDecode := json.Unmarshal(raw, &replay); errDecode != nil || len(replay.Requests) != 3 {
			t.Fatalf("invalid UI request fixture: %v", errDecode)
		}
		for _, req := range replay.Requests {
			post(req.Method, req.URL, req.Body)
		}
	}
	for id, original := range before {
		after, _ := manager.GetByID(id)
		if fetches[id] == 0 || after.ProxyURL != original.ProxyURL || after.Metadata["plugin_owned"] != false {
			t.Errorf("quota fetch altered credential %s", id)
		}
		if active, _ := manager.CredentialConcurrencySnapshot(id); active != 1 {
			t.Errorf("quota fetch changed credential %s occupancy", id)
		}
	}
}
