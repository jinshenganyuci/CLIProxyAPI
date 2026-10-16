package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestV828ManagementSnapshotPreservesCredentialControlsOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `config-version: 8
oauth:
  auth-dir: auths
  providers:
    codex:
      credential-proxy-policy: require
      credential-identity: {enabled: true}
api-keys:
  codex:
    - name: synthetic
      base-url: https://codex.invalid
      keys:
        - api-key: synthetic-key
          proxy-url: socks5h://proxy.invalid:1080
          concurrency-limit: 3
          headers: {X-Synthetic: original}
        - api-key: synthetic-default
          proxy-url: direct
plugins:
  configs:
    opaque: {enabled: false, unknown: {empty: {}, nullable: null}}
`
	if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	input, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	expected := input.CloneForRuntime()
	h := NewHandler(input, path, nil)
	input.CodexKey[0].ProxyURL = "direct"
	*input.CodexKey[0].ConcurrencyLimit = 99
	input.CodexKey[0].Headers["X-Synthetic"] = "mutated"
	input.Codex.CredentialProxyPolicy = "prefer"
	if !reflect.DeepEqual(h.cfg, expected) {
		t.Fatal("management constructor shares mutable credential controls with its caller")
	}
	reloads := make(chan *config.Config, 1)
	h.SetConfigReloadHook(func(_ context.Context, snapshot *config.Config) { reloads <- snapshot })
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	before, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatal(errStat)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v8/management/config/server/max-request-body-bytes", strings.NewReader("4096")))
	if response.Code != http.StatusOK {
		t.Fatalf("config save: %d %s", response.Code, response.Body.String())
	}
	var snapshot *config.Config
	select {
	case snapshot = <-reloads:
	case <-time.After(5 * time.Second):
		t.Fatal("configuration reload did not complete")
	}
	disk, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	after, errStat := os.Stat(path)
	if errStat != nil || !os.SameFile(before, after) {
		t.Fatal("configuration save replaced the mounted file inode")
	}
	var savedPlugin, expectedPlugin any
	savedOpaque, expectedOpaque := disk.Plugins.Configs["opaque"], expected.Plugins.Configs["opaque"]
	if errDecode := savedOpaque.Raw.Decode(&savedPlugin); errDecode != nil {
		t.Fatal(errDecode)
	}
	if errDecode := expectedOpaque.Raw.Decode(&expectedPlugin); errDecode != nil {
		t.Fatal(errDecode)
	}
	// File loads carry a source fence; the management callback is parsed from
	// in-memory bytes. Compare their configuration values independently of provenance.
	snapshotValues, diskValues := snapshot.CloneForRuntime(), disk.CloneForRuntime()
	snapshotValues.SourceHash, diskValues.SourceHash = "", ""
	if !reflect.DeepEqual(snapshotValues, diskValues) || !reflect.DeepEqual(disk.CodexKey, expected.CodexKey) || !reflect.DeepEqual(savedPlugin, expectedPlugin) || !reflect.DeepEqual(disk.Codex, expected.Codex) {
		t.Fatal("saved configuration lost credential controls or opaque plugin settings")
	}
	if disk.CodexKey[1].ConcurrencyLimit != nil || disk.MaxRequestBodyBytes != 4096 {
		t.Fatal("save materialized a default concurrency limit or omitted the requested update")
	}
	h.SetConfig(snapshot)
	snapshot.CodexKey[0].ProxyURL = "direct"
	*snapshot.CodexKey[0].ConcurrencyLimit = 77
	if h.cfg.CodexKey[0].ProxyURL != expected.CodexKey[0].ProxyURL || *h.cfg.CodexKey[0].ConcurrencyLimit != 3 {
		t.Fatal("runtime reload exposed mutable callback-owned credential settings")
	}
}

func TestV828ManagementReloadKeepsOAuthSnapshotsConsistent(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "synthetic-local")
	configs := []*config.Config{{AuthDir: t.TempDir()}, {AuthDir: t.TempDir()}}
	for i, cfg := range configs {
		cfg.ProxyURL = []string{"socks5h://first.invalid:1080", "http://second.invalid:8080"}[i]
		cfg.RemoteManagement.AllowRemote = true
	}
	h := NewHandler(configs[0], "", nil)
	h.SetLocalPassword("synthetic-local")
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(2)
	go func() {
		defer done.Done()
		<-start
		for i := range 100 {
			h.SetConfig(configs[i%len(configs)])
		}
	}()
	go func() {
		defer done.Done()
		<-start
		for range 100 {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v8/management/oauth/codex", strings.NewReader("{}"))
			options, status, errPrepare := h.prepareCodexOAuthSession(ctx)
			if errPrepare != nil || status != http.StatusOK {
				t.Errorf("OAuth snapshot failed: %d %v", status, errPrepare)
				return
			}
			index := 0
			if options.handler.cfg.AuthDir == configs[1].AuthDir {
				index = 1
			}
			if options.handler.cfg.AuthDir != configs[index].AuthDir || options.proxyURL != configs[index].ProxyURL || options.handler.cfg.ProxyURL != options.proxyURL {
				t.Error("OAuth snapshot mixed auth directory and outbound proxy from different reloads")
				return
			}
			if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "synthetic-local"); !allowed {
				t.Error("management authentication failed during a configuration reload")
				return
			}
		}
	}()
	close(start)
	done.Wait()
}
