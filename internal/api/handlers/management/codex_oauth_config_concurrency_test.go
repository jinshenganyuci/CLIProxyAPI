package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCodexOAuthSnapshotDuringGlobalProxyUpdate(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{AuthDir: dir}
	cfg.ProxyURL = "http://127.0.0.1:18080"
	h := &Handler{cfg: cfg, configFilePath: filepath.Join(dir, "config.yaml")}
	if errWrite := os.WriteFile(h.configFilePath, []byte("auth-dir: "+dir+"\nproxy-url: http://127.0.0.1:18080\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(2)
	go func() {
		defer done.Done()
		<-start
		for index := range 30 {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/proxy-url", strings.NewReader(fmt.Sprintf(`{"value":"http://127.0.0.1:%d"}`, 18080+index)))
			ctx.Request.Header.Set("Content-Type", "application/json")
			h.PutProxyURL(ctx)
			if response.Code != http.StatusOK {
				t.Errorf("proxy update failed: %s", response.Body.String())
				return
			}
		}
	}()
	go func() {
		defer done.Done()
		<-start
		for range 100 {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/codex-auth-url", nil)
			options, _, errPrepare := h.prepareCodexOAuthSession(ctx)
			if errPrepare != nil {
				t.Errorf("prepare OAuth snapshot: %v", errPrepare)
				return
			}
			if !strings.HasPrefix(options.proxyURL, "http://127.0.0.1:") || options.handler.cfg.ProxyURL != options.proxyURL {
				t.Errorf("inconsistent OAuth proxy snapshot")
				return
			}
		}
	}()
	close(start)
	done.Wait()
}
