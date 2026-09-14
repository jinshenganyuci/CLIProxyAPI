package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAPICallDevinCredentialProxy(t *testing.T) {
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
					if r.Header.Get("Authorization") != "Bearer synthetic-devin-proxy-token" {
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
				ID: "devin-management", Provider: "devin", ProxyURL: resolve(test.credential),
				Attributes: map[string]string{"api_key": "synthetic-devin-proxy-token"},
				Metadata:   map[string]any{"auth_kind": "oauth", "api_key": "synthetic-devin-proxy-token"},
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
