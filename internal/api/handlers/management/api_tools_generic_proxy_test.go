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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAPICallGenericSelectedCredentialProxy(t *testing.T) {
	for _, provider := range []string{"gemini", "claude", "codex", "xai", "antigravity", "custom"} {
		t.Run(provider, func(t *testing.T) {
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
						ID: "generic-management", Provider: provider, ProxyURL: resolve(test.credential),
						Attributes: map[string]string{"api_key": "synthetic-devin-proxy-token"},
						Metadata:   map[string]any{"auth_kind": "api_key", "access_token": "synthetic-devin-proxy-token", "api_key": "synthetic-devin-proxy-token", "expired": "2099-01-01T00:00:00Z"},
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
		})
	}
}

func TestAPICallSourceConfigProxyCannotBeOverridden(t *testing.T) {
	var directHits, proxyHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-config-key" {
			t.Error("source config credential token was lost")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer proxyServer.Close()
	cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "synthetic-config-key", BaseURL: destination.URL, ProxyURL: proxyServer.URL}}}
	manager := coreauth.NewManager(nil, nil, nil)
	auth, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
		ID: "config-proxy", Provider: "codex", Attributes: map[string]string{"api_key": "synthetic-config-key", "base_url": destination.URL},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	h := &Handler{cfg: cfg, authManager: manager}
	for _, override := range []string{"", "direct"} {
		body, errMarshal := json.Marshal(map[string]any{
			"auth_index": auth.EnsureIndex(), "method": "GET", "url": destination.URL, "proxy_url": override,
			"header": map[string]string{"Authorization": "Bearer $TOKEN$"},
		})
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api-call", bytes.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.APICall(ctx)
		want := http.StatusOK
		if override != "" {
			want = http.StatusConflict
		}
		if recorder.Code != want {
			t.Fatalf("override %q status=%d body=%s", override, recorder.Code, recorder.Body.String())
		}
	}
	if proxyHits.Load() != 1 || directHits.Load() != 0 {
		t.Fatalf("source proxy was bypassed: proxy=%d direct=%d", proxyHits.Load(), directHits.Load())
	}
}
