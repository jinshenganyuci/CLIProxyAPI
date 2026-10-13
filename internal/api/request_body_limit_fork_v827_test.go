package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type bodyCloserV827 struct {
	io.Reader
	closed bool
}

func (b *bodyCloserV827) Close() error { b.closed = true; return nil }

func TestV827UnknownLengthAtLimitPreservesBodyAndCloser(t *testing.T) {
	cfg := parseTestServerConfigYAML(t, "config-version: 8\nserver: {max-request-body-bytes: 16}\n")
	server := newTestServerWithConfig(t, cfg)
	server.engine.POST("/v827-echo", func(c *gin.Context) {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		c.Data(http.StatusOK, "application/octet-stream", body)
	})
	body := &bodyCloserV827{Reader: strings.NewReader("0123456789abcdef")}
	req := httptest.NewRequest(http.MethodPost, "/v827-echo", body)
	req.ContentLength = -1
	response := httptest.NewRecorder()
	server.engine.ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Body.String() != "0123456789abcdef" {
		t.Fatal("a valid unknown-length body at the limit was altered or rejected")
	}
	if errClose := req.Body.Close(); errClose != nil || !body.closed {
		t.Fatal("buffered body did not retain the original closer")
	}
}

func TestV827ChunkedBodyLimitRejectsBeforeCredentialMutation(t *testing.T) {
	for _, tc := range []struct{ name, path, prefix string }{
		{"fields", "/v8/management/credentials/fields", `{"name":"synthetic.json","proxy_url":"direct","concurrency_limit":1}`},
		{"status", "/v8/management/credentials/status", `{"name":"synthetic.json","disabled":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := parseTestServerConfigYAML(t, "config-version: 8\nserver: {max-request-body-bytes: 2048}\nmanagement: {allow-remote: true, secret-key: synthetic-management}\n")
			server := newTestServerWithConfig(t, cfg)
			store := fileauth.NewFileTokenStore()
			store.SetBaseDir(cfg.AuthDir)
			manager := server.handlers.AuthManager
			manager.SetStore(store)
			path := filepath.Join(cfg.AuthDir, "synthetic.json")
			auth, errRegister := manager.Register(t.Context(), &coreauth.Auth{
				ID: "synthetic.json", FileName: "synthetic.json", Provider: "codex", Status: coreauth.StatusActive,
				ProxyURL: "socks5h://127.0.0.1:18081", Attributes: map[string]string{"path": path},
				Metadata: map[string]any{"type": "codex", "access_token": "synthetic", "proxy_url": "socks5h://127.0.0.1:18081", "plugin_owned": map[string]any{"enabled": false}},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			before, _ := manager.GetByID(auth.ID)
			disk, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), auth)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer release()
			counter := &countingBodyReader{reader: strings.NewReader(tc.prefix + strings.Repeat(" ", 4096))}
			req := httptest.NewRequest(http.MethodPatch, tc.path, io.NopCloser(counter))
			req.ContentLength = -1
			req.Header.Set("Authorization", "Bearer synthetic-management")
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.engine.ServeHTTP(response, req)
			if response.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("oversized chunked request returned %d, want 413", response.Code)
			}
			if counter.read.Load() > cfg.MaxRequestBodyBytes+1 {
				t.Error("oversized body was read beyond the configured bound")
			}
			after, _ := manager.GetByID(auth.ID)
			afterDisk, errRead := os.ReadFile(path)
			if errRead != nil || !bytes.Equal(disk, afterDisk) || after.ProxyURL != before.ProxyURL || after.Disabled != before.Disabled || !reflect.DeepEqual(after.Metadata, before.Metadata) {
				t.Error("rejected oversized request changed the credential or persisted metadata")
			}
			if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 1 || limit != 10 {
				t.Errorf("rejected request changed occupancy/default capacity: %d/%d", active, limit)
			}
		})
	}
}
