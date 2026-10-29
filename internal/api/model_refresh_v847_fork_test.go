package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV847ModelRefreshRoutesPreserveAccessAndCredentialControls(t *testing.T) {
	registry.SetLocalModelCatalogs(true)
	defer registry.SetLocalModelCatalogs(false)
	cfg := parseTestServerConfigYAML(t, "config-version: 8\nserver: {max-request-body-bytes: 2048}\nmanagement: {allow-remote: true, secret-key: synthetic-management}\noauth: {providers: {codex: {credential-proxy-policy: require, credential-identity: {enabled: true}}}}\n")
	server := newTestServerWithConfig(t, cfg)
	original := []byte("config-version: 8\n# Synthetic persisted config must remain untouched.\n")
	if err := os.WriteFile(server.configFilePath, original, 0600); err != nil {
		t.Fatal(err)
	}
	manager := server.handlers.AuthManager
	selected, err := manager.Register(t.Context(), &coreauth.Auth{ID: "selected", Provider: "codex", ProxyURL: "direct", Metadata: map[string]any{"concurrency_limit": 3, "type": "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	release, err := manager.AcquireCredentialConcurrency(t.Context(), selected)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, path := range []string{"/v8/management/models/refresh", "/v8/management/routing/model-definitions/refresh"} {
		for _, authorized := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			if authorized {
				req.Header.Set("Authorization", "Bearer synthetic-management")
			}
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)
			if !authorized {
				if rec.Code != 401 {
					t.Fatalf("unauthorized refresh=%d", rec.Code)
				}
				continue
			}
			if rec.Code != 200 {
				t.Fatalf("authorized refresh=%d %s", rec.Code, rec.Body.String())
			}
			var body struct {
				OK       bool                                    `json:"ok"`
				Catalogs map[string]registry.CatalogSourceStatus `json:"catalogs"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.OK || len(body.Catalogs) != 3 {
				t.Fatal("refresh response contract changed")
			}
			for _, catalog := range body.Catalogs {
				if !catalog.Skipped || catalog.Source != "embed" {
					t.Fatal("local catalog policy attempted refresh")
				}
			}
		}
	}
	current, _ := manager.GetByID(selected.ID)
	if current.RegistrationEpoch != selected.RegistrationEpoch || current.ProxyURL != "direct" {
		t.Fatal("model refresh changed selected credential")
	}
	if active, limit := manager.CredentialConcurrencySnapshot(selected.ID); active != 1 || limit != 3 {
		t.Fatalf("model refresh changed lease/limit: %d/%d", active, limit)
	}
	if !server.getConfig().Codex.CredentialIdentity.Enabled || server.getConfig().Codex.CredentialProxyPolicy != "require" {
		t.Fatal("refresh changed fork config")
	}
	saved, err := os.ReadFile(server.configFilePath)
	if err != nil || !bytes.Equal(saved, original) {
		t.Fatal("refresh rewrote config")
	}
}
