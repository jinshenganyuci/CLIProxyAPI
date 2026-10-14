package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

func TestV828RuntimeReloadPreservesCredentialManagementViews(t *testing.T) {
	cfg := parseTestServerConfigYAML(t, "config-version: 8\nmanagement: {allow-remote: true, secret-key: synthetic-management}\n")
	server := newTestServerWithConfig(t, cfg)
	data, errMarshal := yaml.Marshal(server.getConfig())
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(server.configFilePath, data, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	manager := server.handlers.AuthManager
	for i, proxy := range []string{"direct", "socks5h://proxy.invalid:1080", "http://other.invalid:8080"} {
		metadata := map[string]any{"type": "codex", "proxy_url": proxy, "plugin_owned": false}
		if i == 0 {
			metadata["concurrency_limit"] = 3
		} else if i == 2 {
			metadata["concurrency_limit"] = 5
		}
		auth, errRegister := manager.Register(t.Context(), &coreauth.Auth{
			ID: fmt.Sprintf("synthetic-%d.json", i), FileName: fmt.Sprintf("synthetic-%d.json", i),
			Provider: "codex", ProxyURL: proxy, Metadata: metadata,
			Attributes: map[string]string{"runtime_only": "true"},
		})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), auth)
		if errAcquire != nil {
			t.Fatal(errAcquire)
		}
		t.Cleanup(release)
	}
	base := server.getConfig().CloneForRuntime()
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(2)
	go func() {
		defer done.Done()
		<-start
		for i := range 20 {
			next := base.CloneForRuntime()
			next.ProxyURL = fmt.Sprintf("http://unused-global.invalid:%d", 20000+i)
			next.Client.Codex.OptimizeMultiAgentV2 = i%2 == 0
			if !server.UpdateClientsContext(t.Context(), next) {
				t.Error("runtime configuration reload failed")
				return
			}
			next.ProxyURL = "direct"
			if server.getConfig().ProxyURL == "direct" {
				t.Error("server retained caller-owned configuration after reload")
				return
			}
		}
	}()
	go func() {
		defer done.Done()
		<-start
		for range 40 {
			for _, path := range []string{"/v1/models", "/v8/management/credentials", "/v8/management/config"} {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				key := "synthetic-management"
				if path == "/v1/models" {
					key = "test-key"
				}
				request.Header.Set("Authorization", "Bearer "+key)
				response := httptest.NewRecorder()
				server.engine.ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					t.Errorf("%s during reload: status=%d", path, response.Code)
					return
				}
				if path != "/v8/management/credentials" {
					continue
				}
				var payload struct {
					Files []struct {
						ID     string `json:"id"`
						Limit  int    `json:"concurrency_limit"`
						Active int    `json:"concurrency_in_flight"`
					} `json:"files"`
				}
				if errDecode := json.Unmarshal(response.Body.Bytes(), &payload); errDecode != nil || len(payload.Files) != 3 {
					t.Error("credential list disappeared or became invalid during reload")
					return
				}
				limits := map[string]int{"synthetic-0.json": 3, "synthetic-1.json": 10, "synthetic-2.json": 5}
				for _, file := range payload.Files {
					if file.Active != 1 || file.Limit != limits[file.ID] {
						t.Error("runtime reload changed held credential capacity")
						return
					}
				}
			}
		}
	}()
	close(start)
	done.Wait()
	for i, proxy := range []string{"direct", "socks5h://proxy.invalid:1080", "http://other.invalid:8080"} {
		auth, exists := manager.GetByID(fmt.Sprintf("synthetic-%d.json", i))
		if !exists || auth.ProxyURL != proxy || auth.Metadata["plugin_owned"] != false {
			t.Fatal("runtime reload changed a credential proxy or plugin-owned metadata")
		}
	}
}
