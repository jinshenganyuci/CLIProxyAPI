package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV837VertexOpenAPISaveReloadPreservesControls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := fmt.Sprintf("config-version: 8\noauth: {auth-dir: %q, providers: {codex: {credential-proxy-policy: require, credential-identity: {enabled: true}}}}\napi-keys: {vertex: [{name: fixture, base-url: https://vertex.invalid, keys: [{api-key: synthetic, proxy-url: direct, concurrency-limit: 3, openapi: false, openapi-location: us-central1, models: [{name: partner/model, alias: public, openapi: true}]}]}]}\n", dir)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cfg, path, nil)
	w, err := watcher.NewWatcher(path, dir, h.SetConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Stop(); err != nil {
			t.Error(err)
		}
	})
	w.SetConfig(cfg)
	done := make(chan struct{}, 1)
	h.SetConfigReloadHook(func(context.Context, *config.Config) { w.ReloadConfigIfChanged(); done <- struct{}{} })
	r := gin.New()
	r.Any("/v8/management/config", h.ConfigV8)
	r.Any("/v8/management/config/*path", h.ConfigV8)
	request := func(method, route, body, tag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, route, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", tag)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}
	first := request(http.MethodGet, "/v8/management/config", "", "")
	updated := request(http.MethodPut, "/v8/management/config/api-keys/vertex", `[{"name":"fixture","base-url":"https://vertex.invalid","keys":[{"api-key":"synthetic","proxy-url":"direct","concurrency-limit":3,"openapi":true,"openapi-location":" global ","models":[{"name":"partner/model","alias":"public","openapi":false}]}]}]`, first.Header().Get("ETag"))
	if updated.Code != 200 {
		t.Fatalf("save: %d %s", updated.Code, updated.Body.String())
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not reload")
	}
	h.mu.Lock()
	current := h.cfg.CloneForRuntime()
	h.mu.Unlock()
	key := current.VertexCompatAPIKey[0]
	if key.OpenAPI == nil || !*key.OpenAPI || key.OpenAPILocation != "global" || key.Models[0].OpenAPI == nil || *key.Models[0].OpenAPI || key.ProxyURL != "direct" || key.ConcurrencyLimit == nil || *key.ConcurrencyLimit != 3 || current.Codex.CredentialProxyPolicy != "require" || !current.Codex.CredentialIdentity.Enabled {
		t.Fatal("reload lost OpenAPI tri-state or fork controls")
	}
	if modelconfig.ComputeVertexCompatModelsHash(cfg.VertexCompatAPIKey[0].Models) == modelconfig.ComputeVertexCompatModelsHash(key.Models) {
		t.Fatal("model route change reused the old capability hash")
	}
	changes := strings.Join(diff.BuildConfigChangeDetails(cfg, current), "\n")
	for _, field := range []string{"vertex[0].openapi:", "vertex[0].openapi-location:", "vertex[0].models:"} {
		if !strings.Contains(changes, field) {
			t.Fatalf("reload did not describe %s", field)
		}
	}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: current, AuthDir: dir, Now: time.Unix(1700000000, 0), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil || len(auths) != 1 {
		t.Fatalf("synthesize: %v", err)
	}
	if auths[0].Attributes["openapi"] != "true" || auths[0].Attributes["openapi_location"] != "global" || auths[0].ProxyURL != "direct" || coreauth.EffectiveConcurrencyLimit(auths[0]) != 3 {
		t.Fatal("runtime auth lost routing or credential controls")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stale := request(http.MethodPut, "/v8/management/config/api-keys/vertex", `[]`, first.Header().Get("ETag"))
	if stale.Code != 412 {
		t.Fatalf("stale save status=%d", stale.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || current.SourceHash != config.ComputeSourceHash(after) {
		t.Fatal("stale writer changed the accepted config")
	}
	restored, err := config.LoadConfig(path)
	if err != nil || restored.VertexCompatAPIKey[0].Models[0].OpenAPI == nil || *restored.VertexCompatAPIKey[0].Models[0].OpenAPI {
		t.Fatal("restart lost explicit model openapi=false")
	}
}

func TestV837SIWCStatusAndTogglePreserveCredentialState(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "http://global.invalid:8080", "require")
	h.authManager.SetConfig(h.cfg)
	metadata := map[string]any{"type": "codex", "access_token": "outer-secret", "refresh_token": "outer-refresh-secret", "account_id": "account", "proxy_url": "direct", "concurrency_limit": 3, "use_siwc": true, "siwc": map[string]any{"access_token": "nested-secret", "refresh_token": "nested-refresh-secret", "client_id": "oaiapp_synthetic", "expires_at": "2035-01-01T00:00:00Z", "expired": "2000-01-01T00:00:00Z"}}
	if err := codex.SetCredentialIdentity(metadata, "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"); err != nil {
		t.Fatal(err)
	}
	auth, err := h.authManager.Register(context.Background(), &coreauth.Auth{ID: "codex.json", FileName: "codex.json", Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	release, err := h.authManager.AcquireCredentialConcurrency(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	h.siwcOAuthFactory = func(*config.Config) siwcOAuthService {
		t.Error("local status/toggle constructed upstream OAuth client")
		return nil
	}
	r := forkAuthV8Router(h)
	r.GET("/v8/management/oauth/siwc/status", h.GetSIWCStatus)
	path := filepath.Join(h.cfg.AuthDir, "codex.json")
	status := func(want string) {
		t.Helper()
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		old, _ := h.authManager.GetByID(auth.ID)
		rr := forkAuthV8Request(r, http.MethodGet, "/v8/management/oauth/siwc/status?auth_index="+auth.Index, "")
		if rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != `{"status":"`+want+`"}` {
			t.Fatalf("status=%d %s", rr.Code, rr.Body.String())
		}
		after, err := os.ReadFile(path)
		latest, _ := h.authManager.GetByID(auth.ID)
		if err != nil || !bytes.Equal(before, after) || !reflect.DeepEqual(old, latest) {
			t.Fatal("status read mutated persisted or runtime credentials")
		}
	}
	status("valid")
	h.authManager.MarkResult(context.Background(), coreauth.Result{AuthID: auth.ID, CredentialVersion: auth.CredentialVersion, AccessToken: "nested-secret", Provider: "codex", Model: "synthetic-model", Error: &coreauth.Error{HTTPStatus: 401, Code: "unauthorized", Message: "synthetic"}})
	status("invalid")
	for _, enabled := range []bool{false, true} {
		rr := forkAuthV8Request(r, http.MethodPatch, "/v8/management/credentials/fields", fmt.Sprintf(`{"name":"codex.json","auth_index":%q,"use_siwc":%t}`, auth.Index, enabled))
		if rr.Code != 200 {
			t.Fatalf("toggle: %d %s", rr.Code, rr.Body.String())
		}
		if enabled {
			status("invalid")
		} else {
			status("off")
		}
	}
	current, _ := h.authManager.GetByID(auth.ID)
	if current.RejectedSIWCAccessToken != "nested-secret" || current.ProxyURL != "direct" || coreauth.EffectiveConcurrencyLimit(current) != 3 || current.RegistrationEpoch != auth.RegistrationEpoch || current.Metadata["access_token"] != "outer-secret" || !reflect.DeepEqual(current.Metadata["siwc"], metadata["siwc"]) || !codexIdentityFieldsEqual(current.Metadata, metadata) {
		t.Fatal("toggle changed protected fields or forgot the rejected nested token")
	}
	data, err := os.ReadFile(path)
	var disk map[string]any
	if err != nil || json.Unmarshal(data, &disk) != nil || disk["use_siwc"] != true || disk["proxy_url"] != "direct" || !reflect.DeepEqual(disk["siwc"], metadata["siwc"]) || !codexIdentityFieldsEqual(disk, metadata) {
		t.Fatal("toggle did not persist only the selected mode")
	}
	if count, limit := h.authManager.CredentialConcurrencySnapshot(auth.ID); count != 1 || limit != 3 {
		t.Fatalf("held lease changed: %d/%d", count, limit)
	}
	release()
	if count, _ := h.authManager.CredentialConcurrencySnapshot(auth.ID); count != 0 {
		t.Fatal("pre-toggle lease did not release")
	}
	wrong := forkAuthV8Request(r, http.MethodGet, "/v8/management/oauth/siwc/status?name=codex.json&auth_index=wrong", "")
	if wrong.Code != 404 || strings.Contains(wrong.Body.String(), "secret") {
		t.Fatal("mismatched credential index was not isolated")
	}
}
