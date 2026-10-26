package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestV844CompressionSaveReloadPreservesCredentialControls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `config-version: 8
server: {response-compression: {enabled: false, min-bytes: 2048}}
oauth: {providers: {codex: {credential-proxy-policy: require, credential-identity: {enabled: true}}}}
api-keys: {codex: [{base-url: https://codex.invalid/v1, keys: [{api-key: synthetic, proxy-url: direct, concurrency-limit: 3}]}]}
plugins: {configs: {synthetic: {enabled: true, opaque: {count: 9007199254740993}}}}
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg.CloneForRuntime())
	body := strings.Repeat("compressible synthetic body ", 150)
	server.engine.GET("/compressed", func(c *gin.Context) { c.String(http.StatusOK, body) })
	handler := management.NewHandler(cfg, path, nil)
	reloaded := make(chan struct{}, 3)
	handler.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { server.UpdateClients(cfg); reloaded <- struct{}{} })
	router := gin.New()
	router.GET("/config", handler.ConfigV8)
	router.PATCH("/config", handler.ConfigV8)
	for _, enabled := range []bool{true, false, true} {
		get := httptest.NewRecorder()
		router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/config", nil))
		boolValue := "false"
		if enabled {
			boolValue = "true"
		}
		request := httptest.NewRequest(http.MethodPatch, "/config", strings.NewReader(`{"server":{"response-compression":{"enabled":`+boolValue+`,"min-bytes":2048}},"upstream":{"codex":{"websocket-compression":false,"upstream-request-compression":true,"upstream-request-compression-min-bytes":4096}}}`))
		request.Header.Set("If-Match", get.Header().Get("ETag"))
		request.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		router.ServeHTTP(result, request)
		if result.Code != http.StatusOK {
			t.Fatalf("save=%d %s", result.Code, result.Body.String())
		}
		<-reloaded
		loaded, errLoad := config.LoadConfig(path)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		if loaded.EffectiveResponseCompression().IsEnabled() != enabled || loaded.EffectiveResponseCompression().EffectiveMinBytes() != 2048 || loaded.Codex.WebsocketCompressionEnabled() || !loaded.Codex.UpstreamRequestCompression || loaded.Codex.UpstreamRequestCompressionMinBytes != 4096 {
			t.Fatal("compression fields lost through save/reload")
		}
		if loaded.Codex.CredentialProxyPolicy != "require" || !loaded.Codex.CredentialIdentity.Enabled || len(loaded.CodexKey) != 1 || loaded.CodexKey[0].ProxyURL != "direct" || loaded.CodexKey[0].ConcurrencyLimit == nil || *loaded.CodexKey[0].ConcurrencyLimit != 3 {
			t.Fatalf("compression save changed protected credential controls: policy=%s identity=%v keys=%+v", loaded.Codex.CredentialProxyPolicy, loaded.Codex.CredentialIdentity.Enabled, loaded.CodexKey)
		}
		saved, errRead := os.ReadFile(path)
		if errRead != nil || !strings.Contains(string(saved), "9007199254740993") {
			t.Fatal("opaque plugin number changed")
		}
		snapshot := loaded.CloneForRuntime()
		*snapshot.ResponseCompression.Enabled = !enabled
		*snapshot.Codex.WebsocketCompression = true
		if loaded.EffectiveResponseCompression().IsEnabled() != enabled || loaded.Codex.WebsocketCompressionEnabled() {
			t.Fatal("runtime clone aliases compression options")
		}
		req := httptest.NewRequest(http.MethodGet, "/compressed", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		server.engine.ServeHTTP(rec, req)
		encoding := ""
		if enabled {
			encoding = "gzip"
		}
		if rec.Header().Get("Content-Encoding") != encoding || string(compressionTestBody(t, encoding, rec.Body.Bytes())) != body {
			t.Fatal("saved compression setting did not reach actual response")
		}
	}
}
