package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAPICallMetaMintAndRequestUseOneRoute(t *testing.T) {
	for _, tc := range []struct{ name, selected, override, route string }{
		{"credential", "credential", "", "credential"},
		{"inherited_global", "", "", "global"},
		{"direct", "direct", "", "direct"},
		{"request_override_without_persist", "", "credential", "credential"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			manager := coreauth.NewManager(nil, nil, nil)
			h := &Handler{cfg: cfg, authManager: manager}
			counts := map[string]*atomic.Int32{"direct": {}, "credential": {}, "global": {}, "changed": {}}
			var changedProxy string
			serve := func(route string) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					counts[route].Add(1)
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/mint" {
						h.mu.Lock()
						cfg.ProxyURL = changedProxy
						h.mu.Unlock()
						_, _ = w.Write([]byte(`{"api_key":"LLM|synthetic-minted-key"}`))
						return
					}
					var data map[string]string
					if errDecode := json.NewDecoder(r.Body).Decode(&data); errDecode != nil {
						t.Error(errDecode)
					}
					if data["token"] != "LLM|synthetic-minted-key" || r.Header.Get("Authorization") != "Bearer LLM|synthetic-minted-key" {
						t.Error("request did not use the minted token in both header and body")
					}
					_, _ = w.Write([]byte(`{"ok":true}`))
				}))
			}
			destination := serve("direct")
			defer destination.Close()
			credential := serve("credential")
			defer credential.Close()
			global := serve("global")
			defer global.Close()
			changed := serve("changed")
			defer changed.Close()
			h.mu.Lock()
			cfg.ProxyURL, changedProxy = global.URL, changed.URL
			h.mu.Unlock()
			t.Setenv("META_MINT_URL", destination.URL+"/mint")
			resolve := func(route string) string {
				if route == "credential" {
					return credential.URL
				}
				return route
			}
			originalProxy := resolve(tc.selected)
			auth, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
				ID: "meta-mint-probe", Provider: "meta", ProxyURL: originalProxy,
				Metadata: map[string]any{"type": "meta", "auth_kind": "oauth", "dca_token": "dca:synthetic-device-key"},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			body, errMarshal := json.Marshal(map[string]any{
				"authIndex": auth.EnsureIndex(), "method": "POST", "url": destination.URL + "/probe",
				"proxy_url": resolve(tc.override), "header": map[string]string{"Authorization": "Bearer $TOKEN$"},
				"data": `{"token":"$TOKEN$"}`,
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			router := gin.New()
			router.POST("/api-call", h.APICall)
			r := httptest.NewRequest(http.MethodPost, "/api-call", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			for route, count := range counts {
				var want int32
				if route == tc.route {
					want = 2
				}
				if count.Load() != want {
					t.Errorf("route=%s requests=%d want=%d", route, count.Load(), want)
				}
			}
			stored, ok := manager.GetByID(auth.ID)
			if !ok || stored.ProxyURL != originalProxy || stored.Metadata["api_key"] != "LLM|synthetic-minted-key" {
				t.Error("mint changed the canonical proxy or failed to install the key")
			}
		})
	}
}

func TestAPICallMetaConfigProxyCannotBeOverridden(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := &config.Config{MetaKey: []config.MetaKey{{APIKey: "LLM|config-key", ProxyURL: server.URL}}}
	manager := coreauth.NewManager(nil, nil, nil)
	auth, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
		ID: "meta-config-proxy", Provider: "meta", Attributes: map[string]string{"api_key": "LLM|config-key"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	h := &Handler{cfg: cfg, authManager: manager}
	router := gin.New()
	router.POST("/api-call", h.APICall)
	body := `{"authIndex":"` + auth.EnsureIndex() + `","method":"GET","url":"` + server.URL + `","proxy_url":"direct"}`
	r := httptest.NewRequest(http.MethodPost, "/api-call", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusConflict || hits.Load() != 0 {
		t.Fatalf("config proxy override status=%d network=%d", w.Code, hits.Load())
	}
}

func TestAPICallMetaCredentialProxy(t *testing.T) {
	for _, test := range []struct {
		name, credential, global, override, route string
		status                                    int
	}{
		{name: "selected", credential: "credential", global: "global", route: "credential"},
		{name: "same_override", credential: "credential", global: "global", override: "credential", route: "credential"},
		{name: "reject_direct_override", credential: "credential", global: "global", override: "direct", status: http.StatusConflict},
		{name: "reject_proxy_override", credential: "credential", global: "global", override: "global", status: http.StatusConflict},
		{name: "invalid_credential", credential: "invalid", global: "global", status: http.StatusConflict},
		{name: "invalid_cannot_override", credential: "invalid", override: "direct", status: http.StatusConflict},
		{name: "unreachable", credential: "unreachable", global: "global", status: http.StatusBadGateway},
		{name: "explicit_direct", credential: "direct", global: "global", route: "direct"},
		{name: "direct_alias", credential: "direct", global: "global", override: "none", route: "direct"},
		{name: "invalid_global_ignored", credential: "credential", global: "invalid", route: "credential"},
		{name: "legacy_inherits", global: "global", route: "global"},
		{name: "legacy_invalid_global", global: "invalid", status: http.StatusConflict},
		{name: "legacy_direct", route: "direct"},
	} {
		t.Run(test.name, func(t *testing.T) {
			counters := map[string]*atomic.Int32{"direct": {}, "credential": {}, "global": {}}
			serve := func(route string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					counters[route].Add(1)
					if r.Header.Get("Authorization") != "Bearer synthetic-meta-proxy-token" {
						t.Error("selected credential token was not supplied")
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				}
			}
			destination := httptest.NewServer(serve("direct"))
			defer destination.Close()
			credential := httptest.NewServer(serve("credential"))
			defer credential.Close()
			global := httptest.NewServer(serve("global"))
			defer global.Close()
			unreachable := httptest.NewServer(serve("credential"))
			unreachable.Close()
			resolve := func(route string) string {
				switch route {
				case "credential":
					return credential.URL
				case "global":
					return global.URL
				case "unreachable":
					return unreachable.URL
				default:
					return route
				}
			}
			cfg := &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: config.CodexCredentialProxyPolicyRequire}}
			cfg.ProxyURL = resolve(test.global)
			manager := coreauth.NewManager(nil, nil, nil)
			auth, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
				ID: "meta-management", Provider: "meta", ProxyURL: resolve(test.credential),
				Attributes: map[string]string{"api_key": "synthetic-meta-proxy-token"},
				Metadata:   map[string]any{"auth_kind": "oauth", "api_key": "synthetic-meta-proxy-token"},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			body, errMarshal := json.Marshal(map[string]any{
				"auth_index": auth.EnsureIndex(), "method": "GET", "url": destination.URL,
				"proxy_url": resolve(test.override), "header": map[string]string{"Authorization": "Bearer $TOKEN$"},
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			h := &Handler{cfg: cfg, authManager: manager}
			router := gin.New()
			router.POST("/api-call", h.APICall)
			request := httptest.NewRequest(http.MethodPost, "/api-call", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			wantStatus := test.status
			if wantStatus == 0 {
				wantStatus = http.StatusOK
			}
			if recorder.Code != wantStatus {
				t.Fatalf("HTTP status = %d, want %d: %s", recorder.Code, wantStatus, recorder.Body.String())
			}
			for route, counter := range counters {
				var want int32
				if route == test.route {
					want = 1
				}
				if got := counter.Load(); got != want {
					t.Errorf("%s requests = %d, want %d", route, got, want)
				}
			}
		})
	}
}
