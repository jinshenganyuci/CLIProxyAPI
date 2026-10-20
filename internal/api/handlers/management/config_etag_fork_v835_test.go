package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
)

func TestV835ETagSaveReloadPreservesForkControlsAndOptionalSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := fmt.Sprintf("config-version: 8\noauth: {auth-dir: %q, providers: {codex: {credential-proxy-policy: require, credential-identity: {enabled: true}}}}\napi-keys: {codex: [{name: fixture, base-url: https://codex.invalid, keys: [{api-key: synthetic, proxy-url: direct, concurrency-limit: 3}]}]}\nplugins: {configs: {opaque: {enabled: false, nullable: null, empty: {}}}}\n", dir)
	if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h := NewHandler(cfg, path, nil)
	w, errWatcher := watcher.NewWatcher(path, dir, h.SetConfig)
	if errWatcher != nil {
		t.Fatal(errWatcher)
	}
	t.Cleanup(func() {
		if errStop := w.Stop(); errStop != nil {
			t.Error(errStop)
		}
	})
	w.SetConfig(cfg)
	done := make(chan struct{}, 2)
	h.SetConfigReloadHook(func(context.Context, *config.Config) { w.ReloadConfigIfChanged(); done <- struct{}{} })
	r := gin.New()
	r.GET("/v8/management/config.yaml", h.ConfigV8)
	r.PUT("/v8/management/config.yaml", h.ConfigV8)
	r.PATCH("/v8/management/config", h.ConfigV8)
	request := func(method, route, body, etag string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, route, strings.NewReader(body))
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}
	old := request(http.MethodGet, "/v8/management/config.yaml", "", "")
	if old.Code != http.StatusOK || old.Header().Get("ETag") == "" {
		t.Fatal("config read did not return its source version")
	}
	saved := request(http.MethodPatch, "/v8/management/config", `{"upstream":{"claude":{"session-date-pin-days":0}},"client":{"codex":{"web-search-result-cache":{"enabled":true,"ttl":1800,"limit":7}}}}`, old.Header().Get("ETag"))
	if saved.Code != http.StatusOK {
		t.Fatalf("conditional save: %d %s", saved.Code, saved.Body.String())
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("saved configuration did not reload")
	}
	before, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	stale := request(http.MethodPut, "/v8/management/config.yaml", raw, old.Header().Get("ETag"))
	if stale.Code != http.StatusPreconditionFailed || stale.Header().Get("ETag") != saved.Header().Get("ETag") {
		t.Fatal("stale whole-document save was not rejected with the current version")
	}
	after, errRead := os.ReadFile(path)
	if errRead != nil || string(before) != string(after) {
		t.Fatal("rejected save changed persisted configuration")
	}
	h.mu.Lock()
	current := h.cfg.CloneForRuntime()
	h.mu.Unlock()
	if current.Codex.CredentialProxyPolicy != "require" || !current.Codex.CredentialIdentity.Enabled || len(current.CodexKey) != 1 || current.CodexKey[0].ProxyURL != "direct" || current.CodexKey[0].ConcurrencyLimit == nil || *current.CodexKey[0].ConcurrencyLimit != 3 || current.Claude.SessionDatePinRetention() != 0 || !current.Client.Codex.WebSearchResultCache.Enabled || current.Client.Codex.WebSearchResultCache.EffectiveTTL() != 30*time.Minute || current.Client.Codex.WebSearchResultCache.EffectiveLimit() != 7 {
		t.Fatal("conditional save and watcher reload lost fork controls or new optional settings")
	}
	if current.SourceHash != config.ComputeSourceHash(after) {
		t.Fatal("runtime source fence does not match the accepted ETag")
	}
}
