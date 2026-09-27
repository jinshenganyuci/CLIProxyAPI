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

func TestQuotaProbeCodexCredentialProxyPolicy(t *testing.T) {
	for _, test := range []struct {
		name            string
		policy          string
		credentialProxy string
		globalProxy     string
		wantRoute       string
	}{
		{name: "require_missing_with_global", globalProxy: "global"},
		{name: "require_missing_without_global"},
		{name: "require_invalid_with_global", credentialProxy: "invalid-proxy", globalProxy: "global"},
		{name: "require_invalid_without_global", credentialProxy: "invalid-proxy"},
		{name: "prefer_invalid_with_global", policy: "prefer", credentialProxy: "invalid-proxy", globalProxy: "global"},
		{name: "prefer_invalid_without_global", policy: "prefer", credentialProxy: "invalid-proxy"},
		{name: "prefer_invalid_global", policy: "prefer", globalProxy: "invalid-global-proxy"},
		{name: "selected_proxy_overrides_global_and_browser", credentialProxy: "credential", globalProxy: "global", wantRoute: "credential"},
		{name: "selected_proxy_overrides_invalid_global", credentialProxy: "credential", globalProxy: "invalid-global-proxy", wantRoute: "credential"},
		{name: "explicit_direct_overrides_global", credentialProxy: "direct", globalProxy: "global", wantRoute: "direct"},
		{name: "prefer_legacy_inherits_global", policy: "prefer", globalProxy: "global", wantRoute: "global"},
		{name: "prefer_legacy_without_global", policy: "prefer", wantRoute: "direct"},
		{name: "unreachable_proxy_never_falls_back", credentialProxy: "unreachable", globalProxy: "global"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var directHits, credentialHits, globalHits, tokenHits atomic.Int32
			serveQuota := func(hits *atomic.Int32) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					if r.Header.Get("Authorization") == "Bearer synthetic-quota-probe-token" {
						tokenHits.Add(1)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"subscription":{"plan":"Plus"}}`))
				}
			}
			destination := httptest.NewServer(serveQuota(&directHits))
			defer destination.Close()
			credentialProxy := httptest.NewServer(serveQuota(&credentialHits))
			defer credentialProxy.Close()
			globalProxy := httptest.NewServer(serveQuota(&globalHits))
			defer globalProxy.Close()
			unreachableProxy := httptest.NewServer(serveQuota(&credentialHits))
			unreachableProxy.Close()
			resolveProxy := func(route string) string {
				switch route {
				case "credential":
					return credentialProxy.URL
				case "global":
					return globalProxy.URL
				case "unreachable":
					return unreachableProxy.URL
				default:
					return route
				}
			}
			policy := test.policy
			if policy == "" {
				policy = config.CodexCredentialProxyPolicyRequire
			}
			cfg := &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: policy}}
			cfg.ProxyURL = resolveProxy(test.globalProxy)
			manager := coreauth.NewManager(nil, nil, nil)
			auth, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
				ID: "quota-probe-credential", Provider: "codex", ProxyURL: resolveProxy(test.credentialProxy),
				Metadata: map[string]any{
					"access_token": "synthetic-quota-probe-token",
					"quota_probe": map[string]any{
						"url":    destination.URL + "/quota",
						"header": map[string]any{"Authorization": "Bearer $TOKEN$"},
					},
				},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			h := &Handler{cfg: cfg, authManager: manager}
			body, errMarshal := json.Marshal(map[string]any{
				"auth_index": auth.EnsureIndex(), "proxy_url": "direct", "provider": "browser-provider-override",
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			rec := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/quota/fetch", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			router := gin.New()
			router.POST("/quota/fetch", h.FetchCredentialQuota)
			router.ServeHTTP(rec, request)
			wantStatus := http.StatusOK
			if test.wantRoute == "" {
				wantStatus = http.StatusBadGateway
			}
			if rec.Code != wantStatus {
				t.Errorf("status = %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
			}
			for route, hits := range map[string]*atomic.Int32{"direct": &directHits, "credential": &credentialHits, "global": &globalHits} {
				var want int32
				if route == test.wantRoute {
					want = 1
				}
				if got := hits.Load(); got != want {
					t.Errorf("%s requests = %d, want %d", route, got, want)
				}
			}
			var wantTokens int32
			if test.wantRoute != "" {
				wantTokens = 1
			}
			if got := tokenHits.Load(); got != wantTokens {
				t.Errorf("credential-bearing requests = %d, want %d", got, wantTokens)
			}
			if strings.Contains(rec.Body.String(), "synthetic-quota-probe-token") {
				t.Fatal("response exposed credential token")
			}
		})
	}
}

func TestQuotaProbeValidatesProxyBeforeResolvingToken(t *testing.T) {
	h := &Handler{cfg: &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: config.CodexCredentialProxyPolicyRequire}}}
	auth := &coreauth.Auth{ID: "missing-token", Provider: "codex", ProxyURL: "invalid-proxy"}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/quota/fetch", nil)
	_, handled, errProbe := h.executeQuotaProbe(c, auth, map[string]any{
		"url": "http://unused.invalid/quota", "header": map[string]any{"Authorization": "Bearer $TOKEN$"},
	})
	if !handled || errProbe == nil || !strings.Contains(errProbe.Error(), "proxy is invalid") {
		t.Fatalf("handled = %v, error = %v; want proxy validation before missing-token resolution", handled, errProbe)
	}
}
