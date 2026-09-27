package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestManagementV8ForkRoutesPreserveAccessAndLegacyFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"authorized", "missing-key", "disabled", "home"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, []byte("port: 8317\nremote-management: {secret-key: synthetic-v8-route-key}\n"), 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			cfg, errLoad := config.LoadConfig(path)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			cfg.AuthDir = t.TempDir()
			cfg.Home.Enabled = mode == "home"
			manager := coreauth.NewManager(nil, nil, nil)
			if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
				ID: "v8-default.json", FileName: "v8-default.json", Provider: "codex", Status: coreauth.StatusActive,
				Attributes: map[string]string{"path": filepath.Join(cfg.AuthDir, "v8-default.json")},
			}); errRegister != nil {
				t.Fatal(errRegister)
			}
			h := management.NewHandler(cfg, path, manager)
			h.SetLocalPassword("synthetic-v8-route-key")
			s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
			s.managementRoutesEnabled.Store(mode != "disabled")
			s.registerManagementRoutes()
			baseline, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			for _, route := range []struct{ method, path, body string }{
				{http.MethodGet, "/v8/management/credentials/concurrency", ""},
				{http.MethodGet, "/v0/management/auth-files/concurrency", ""},
				{http.MethodPost, "/v8/management/oauth/auth-url?provider=codex", `{"proxy_url":"direct"}`},
				{http.MethodPost, "/v0/management/codex-auth-url", `{"proxy_url":"direct"}`},
			} {
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				req.RemoteAddr = "127.0.0.1:1234"
				req.Header.Set("Content-Type", "application/json")
				if mode != "missing-key" {
					req.Header.Set("Authorization", "Bearer synthetic-v8-route-key")
				}
				response := httptest.NewRecorder()
				s.engine.ServeHTTP(response, req)
				want := http.StatusOK
				if mode == "missing-key" {
					want = http.StatusUnauthorized
				} else if mode == "disabled" || mode == "home" {
					want = http.StatusNotFound
				}
				if response.Code != want {
					t.Fatalf("%s %s status=%d want=%d body=%s", route.method, route.path, response.Code, want, response.Body.String())
				}
				if want != http.StatusOK {
					continue
				}
				if route.method == http.MethodPost {
					var login struct{ State, URL string }
					if errDecode := json.Unmarshal(response.Body.Bytes(), &login); errDecode != nil {
						t.Fatal(errDecode)
					}
					t.Cleanup(func() { management.CancelOAuthSession(login.State) })
					if login.State == "" || login.URL == "" || !management.IsOAuthSessionPending(login.State, "codex") {
						t.Fatal("proxy-aware OAuth route did not create a pending login")
					}
				} else {
					var counters struct {
						Entries []struct {
							ID       string
							Limit    int `json:"concurrency_limit"`
							InFlight int `json:"concurrency_in_flight"`
						}
					}
					if errDecode := json.Unmarshal(response.Body.Bytes(), &counters); errDecode != nil {
						t.Fatal(errDecode)
					}
					if len(counters.Entries) != 1 || counters.Entries[0].ID != "v8-default.json" || counters.Entries[0].Limit != 10 || counters.Entries[0].InFlight != 0 {
						t.Fatalf("default credential counters lost: %s", response.Body.String())
					}
				}
			}
			after, errAfter := os.ReadFile(path)
			if errAfter != nil || string(after) != string(baseline) {
				t.Fatalf("operational routes migrated legacy configuration: %v", errAfter)
			}
		})
	}
}
