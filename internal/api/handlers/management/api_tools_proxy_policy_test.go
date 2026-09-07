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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPICallCodexCredentialProxyPolicy(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name            string
		policy          string
		provider        string
		credentialProxy string
		requestProxy    string
		global          bool
		invalidGlobal   bool
		apiKey          bool
		wantStatus      int
		wantRoute       string
	}{
		{name: "missing_with_global", credentialProxy: "", global: true, wantStatus: http.StatusConflict},
		{name: "missing_without_global", credentialProxy: "", wantStatus: http.StatusConflict},
		{name: "invalid_with_global", credentialProxy: "invalid-proxy", global: true, wantStatus: http.StatusConflict},
		{name: "invalid_without_global", credentialProxy: "invalid-proxy", wantStatus: http.StatusConflict},
		{name: "direct_override_rejected", credentialProxy: "credential", requestProxy: "direct", global: true, wantStatus: http.StatusConflict},
		{name: "different_proxy_rejected", credentialProxy: "credential", requestProxy: "global", global: true, wantStatus: http.StatusConflict},
		{name: "proxy_cannot_replace_direct", credentialProxy: "direct", requestProxy: "global", global: true, wantStatus: http.StatusConflict},
		{name: "credential_proxy_used", credentialProxy: "credential", global: true, wantStatus: http.StatusOK, wantRoute: "credential"},
		{name: "matching_override_allowed", credentialProxy: "credential", requestProxy: "credential", global: true, wantStatus: http.StatusOK, wantRoute: "credential"},
		{name: "explicit_direct_allowed", credentialProxy: "direct", global: true, wantStatus: http.StatusOK, wantRoute: "direct"},
		{name: "direct_alias_allowed", credentialProxy: "direct", requestProxy: "none", global: true, wantStatus: http.StatusOK, wantRoute: "direct"},
		{name: "unreachable_never_falls_back", credentialProxy: "unreachable", global: true, wantStatus: http.StatusBadGateway},
		{name: "prefer_retains_inheritance", policy: "prefer", global: true, wantStatus: http.StatusOK, wantRoute: "global"},
		{name: "prefer_rejects_credential_override", policy: "prefer", credentialProxy: "credential", requestProxy: "direct", global: true, wantStatus: http.StatusConflict},
		{name: "prefer_retains_unbound_override", policy: "prefer", requestProxy: "direct", global: true, wantStatus: http.StatusOK, wantRoute: "direct"},
		{name: "prefer_matching_override", policy: "prefer", credentialProxy: "credential", requestProxy: "credential", global: true, wantStatus: http.StatusOK, wantRoute: "credential"},
		{name: "prefer_direct_alias", policy: "prefer", credentialProxy: "direct", requestProxy: "none", global: true, wantStatus: http.StatusOK, wantRoute: "direct"},
		{name: "prefer_invalid_credential", policy: "prefer", credentialProxy: "invalid-proxy", global: true, wantStatus: http.StatusConflict},
		{name: "prefer_invalid_global", policy: "prefer", invalidGlobal: true, wantStatus: http.StatusConflict},
		{name: "prefer_unreachable_credential", policy: "prefer", credentialProxy: "unreachable", global: true, wantStatus: http.StatusBadGateway},
		{name: "prefer_credential_overrides_invalid_global", policy: "prefer", credentialProxy: "credential", invalidGlobal: true, wantStatus: http.StatusOK, wantRoute: "credential"},
		{name: "prefer_unbound_request_overrides_invalid_global", policy: "prefer", requestProxy: "credential", invalidGlobal: true, wantStatus: http.StatusOK, wantRoute: "credential"},
		{name: "other_provider_retains_inheritance", provider: "claude", global: true, wantStatus: http.StatusOK, wantRoute: "global"},
		{name: "api_key_retains_inheritance", apiKey: true, global: true, wantStatus: http.StatusOK, wantRoute: "global"},
		{name: "other_provider_retains_override", provider: "claude", credentialProxy: "credential", requestProxy: "direct", global: true, wantStatus: http.StatusOK, wantRoute: "direct"},
		{name: "api_key_retains_override", apiKey: true, credentialProxy: "credential", requestProxy: "direct", global: true, wantStatus: http.StatusOK, wantRoute: "direct"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var directHits, credentialHits, globalHits, tokenHits atomic.Int32
			handler := func(counter *atomic.Int32) http.HandlerFunc {
				return func(writer http.ResponseWriter, request *http.Request) {
					counter.Add(1)
					if request.Header.Get("Authorization") == "Bearer synthetic-quota-token" {
						tokenHits.Add(1)
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(`{"plan_type":"plus"}`))
				}
			}
			destination := httptest.NewServer(handler(&directHits))
			defer destination.Close()
			credentialProxy := httptest.NewServer(handler(&credentialHits))
			defer credentialProxy.Close()
			globalProxy := httptest.NewServer(handler(&globalHits))
			defer globalProxy.Close()
			unreachableProxy := httptest.NewServer(handler(&credentialHits))
			unreachableProxy.Close()
			resolveProxy := func(value string) string {
				switch value {
				case "credential":
					return credentialProxy.URL
				case "global":
					return globalProxy.URL
				case "unreachable":
					return unreachableProxy.URL
				default:
					return value
				}
			}
			policy := test.policy
			if policy == "" {
				policy = config.CodexCredentialProxyPolicyRequire
			}
			cfg := &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: policy}}
			if test.global {
				cfg.SDKConfig = sdkconfig.SDKConfig{ProxyURL: globalProxy.URL}
			}
			if test.invalidGlobal {
				cfg.ProxyURL = "invalid-global-proxy"
			}
			provider := test.provider
			if provider == "" {
				provider = "codex"
			}
			auth := &coreauth.Auth{
				ID: "quota-credential", Provider: provider, ProxyURL: resolveProxy(test.credentialProxy),
				Metadata: map[string]any{"access_token": "synthetic-quota-token"},
			}
			if test.apiKey {
				auth.Attributes = map[string]string{"api_key": "synthetic-quota-token"}
				auth.Metadata = nil
			}
			manager := coreauth.NewManager(nil, nil, nil)
			registered, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			h := &Handler{cfg: cfg, authManager: manager}
			body, errMarshal := json.Marshal(map[string]any{
				"authIndex": registered.EnsureIndex(), "method": "GET",
				"url": destination.URL + "/backend-api/wham/usage", "proxy_url": resolveProxy(test.requestProxy),
				"header": map[string]string{"Authorization": "Bearer $TOKEN$"},
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api-call", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			router := gin.New()
			router.POST("/api-call", h.APICall)
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			for route, counter := range map[string]*atomic.Int32{"direct": &directHits, "credential": &credentialHits, "global": &globalHits} {
				var want int32
				if route == test.wantRoute {
					want = 1
				}
				if got := counter.Load(); got != want {
					t.Errorf("%s requests = %d, want %d", route, got, want)
				}
			}
			var wantTokens int32
			if test.wantStatus == http.StatusOK {
				wantTokens = 1
			}
			if tokenHits.Load() != wantTokens {
				t.Errorf("credential-bearing requests = %d, want %d", tokenHits.Load(), wantTokens)
			}
			if strings.Contains(recorder.Body.String(), "synthetic-quota-token") {
				t.Fatal("management response exposed token")
			}
		})
	}
}
