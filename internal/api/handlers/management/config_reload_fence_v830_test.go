package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
)

func TestV830ManagementSaveFenceAndWatcherReloadCompose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := fmt.Sprintf("config-version: 8\noauth: {auth-dir: %q, providers: {codex: {credential-proxy-policy: require, credential-identity: {enabled: true}}}}\nrequests: {tls-handshake-timeout-seconds: 0}\nupstream: {claude: {header-defaults: {inject-diagnostics: false, anthropic-beta-append: [synthetic-beta]}}}\n", dir)
	if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h := NewHandler(cfg, path, nil)
	entered, release, firstDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	resume := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(resume)
	var callbacks atomic.Int32
	var w *watcher.Watcher
	w, errLoad = watcher.NewWatcher(path, dir, func(snapshot *config.Config) {
		call := callbacks.Add(1)
		if call == 1 {
			close(entered)
			<-release
		}
		h.SetConfig(snapshot)
		if call == 1 {
			// Check before the next serialized reload can repair a stale publication.
			h.mu.Lock()
			savedTimeout := h.cfg.TLSHandshakeTimeoutSeconds
			h.mu.Unlock()
			if savedTimeout != 7 {
				t.Errorf("stale watcher callback replaced saved TLS bound with %d", savedTimeout)
			}
		}
		w.SetConfig(snapshot)
	})
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	t.Cleanup(func() {
		if errStop := w.Stop(); errStop != nil {
			t.Error(errStop)
		}
	})
	w.SetConfig(cfg)
	reloaded := make(chan struct{}, 2)
	h.SetConfigReloadHook(func(_ context.Context, _ *config.Config) {
		w.ReloadConfigIfChanged()
		reloaded <- struct{}{}
	})
	go func() {
		defer close(firstDone)
		w.ReloadConfigIfChanged()
	}()
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("reload synchronization did not complete")
		}
	}
	await(entered)
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	put := func(route, body string) {
		t.Helper()
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, route, strings.NewReader(body)))
		if rr.Code != http.StatusOK {
			t.Fatalf("save %s: %d %s", route, rr.Code, rr.Body.String())
		}
	}
	put("/v8/management/config/requests/tls-handshake-timeout-seconds", "7")
	resume()
	await(firstDone)
	await(reloaded)
	// The stale first callback must not replace the management config saved above.
	h.mu.Lock()
	timeout := h.cfg.TLSHandshakeTimeoutSeconds
	h.mu.Unlock()
	if timeout != 7 {
		t.Fatalf("stale watcher callback rolled back saved TLS bound to %d", timeout)
	}
	put("/v8/management/config/upstream/claude/header-defaults/client-request-id", `"off"`)
	await(reloaded)
	disk, errReload := config.LoadConfig(path)
	if errReload != nil {
		t.Fatal(errReload)
	}
	h.mu.Lock()
	snapshot := h.cfg.CloneForRuntime()
	h.mu.Unlock()
	for _, actual := range []*config.Config{disk, snapshot} {
		if actual.TLSHandshakeTimeoutSeconds != 7 || actual.ClaudeHeaderDefaults.ClientRequestID != "off" || actual.ClaudeHeaderDefaults.InjectDiagnostics == nil || *actual.ClaudeHeaderDefaults.InjectDiagnostics || actual.ClaudeHeaderDefaults.AnthropicBetaAppend[0] != "synthetic-beta" || actual.Codex.CredentialProxyPolicy != "require" || !actual.Codex.CredentialIdentity.Enabled {
			t.Fatal("save/reload lost new header options or fork credential controls")
		}
	}
	if snapshot.SourceHash != disk.SourceHash {
		t.Fatal("management snapshot does not identify the final applied file")
	}
	before := callbacks.Load()
	w.ReloadConfigIfChanged()
	if callbacks.Load() != before {
		t.Fatal("watcher did not acknowledge the final applied source")
	}
}
