package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func TestConfigV808DeletePreservesForkPoliciesAndDocumentPresence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const original = `config-version: 8
requests: {proxy-url: socks5h://127.0.0.1:1080}
oauth:
  settings:
    codex: [{name: gpt-6-sol, max-context-length: 524288}]
  providers:
    codex:
      credential-proxy-policy: require
      credential-identity: {enabled: true}
api-keys:
  codex:
    - name: synthetic
      base-url: https://example.invalid/v1
      keys:
        - {api-key: synthetic-a, proxy-url: direct, concurrency-limit: 3}
        - {api-key: synthetic-b, proxy-url: direct, concurrency-limit: null}
        - {api-key: synthetic-c, proxy-url: direct}
plugins:
  configs:
    cpa-access-guard:
      enabled: true
      options: {zero: 0, off: false, empty: {}, items: [], absent: null}
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(original), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	beforeInfo, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatal(errStat)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.DELETE("/v8/management/config/*path", h.ConfigV8)
	request := func(endpoint string, want int) {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, endpoint, nil))
		if response.Code != want {
			t.Fatalf("DELETE %s: status=%d body=%s", endpoint, response.Code, response.Body.String())
		}
	}
	request("/v8/management/config/oauth/settings/codex", http.StatusOK)
	saved, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	afterInfo, errStat := os.Stat(path)
	if errStat != nil || !os.SameFile(beforeInfo, afterInfo) {
		t.Fatalf("DELETE replaced the mounted configuration inode: %v", errStat)
	}
	var want, got map[string]any
	if errDecode := yaml.Unmarshal([]byte(original), &want); errDecode != nil {
		t.Fatal(errDecode)
	}
	if errDecode := yaml.Unmarshal(saved, &got); errDecode != nil {
		t.Fatal(errDecode)
	}
	// Deleting the last child removes its now-empty ancestor, as in the native API.
	delete(want["oauth"].(map[string]any), "settings")
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("DELETE changed unrelated configuration presence or values: %s", saved)
	}
	reloaded, errReload := config.LoadConfig(path)
	if errReload != nil {
		t.Fatal(errReload)
	}
	for _, value := range []*config.Config{h.cfg, reloaded} {
		if len(value.OAuthSettings) != 0 || value.ProxyURL != cfg.ProxyURL || value.Codex.CredentialProxyPolicy != "require" || !value.Codex.CredentialIdentity.Enabled || len(value.CodexKey) != 3 {
			t.Fatal("DELETE changed runtime fork policies or retained a removed model override")
		}
		for i, key := range value.CodexKey {
			if key.ProxyURL != "direct" || (i == 0 && (key.ConcurrencyLimit == nil || *key.ConcurrencyLimit != 3)) || (i > 0 && key.ConcurrencyLimit != nil) {
				t.Fatalf("credential %d lost proxy or explicit/default concurrency", i)
			}
		}
	}
	request("/v8/management/config/oauth/settings/codex", http.StatusNotFound)
	afterRejected, errRead := os.ReadFile(path)
	if errRead != nil || !bytes.Equal(saved, afterRejected) {
		t.Fatalf("rejected deletion changed file: %v", errRead)
	}
}
