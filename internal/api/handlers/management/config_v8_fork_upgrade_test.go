package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

func TestConfigV8ForkMigrationManagementContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const raw = `# Preserve the fork deployment settings.
port: 8317
api-keys: [synthetic-client]
proxy-url: socks5h://127.0.0.1:1080
codex:
  credential-proxy-policy: require
  credential-identity: {enabled: true}
codex-api-key:
  - api-key: synthetic-key
    base-url: https://example.invalid/v1
    proxy-url: direct
    concurrency-limit: 3
plugins:
  configs:
    cpa-access-guard:
      enabled: true
      options: {image-billing: {price: 0, enabled: false}, custom: null}
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.GET("/v0/management/config", h.GetConfig)
	router.GET("/v0/management/config.yaml", h.GetConfigYAML)
	router.PUT("/v0/management/codex-api-key", h.PutCodexKeys)
	router.GET("/v8/management/config", h.ConfigV8)
	router.GET("/v8/management/config.yaml", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)
	request := func(method, url, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
		if recorder.Code != status {
			t.Fatalf("%s %s returned %d: %s", method, url, recorder.Code, recorder.Body.String())
		}
		return recorder
	}
	assertUnchanged := func(want []byte) {
		t.Helper()
		got, errRead := os.ReadFile(path)
		if errRead != nil || !bytes.Equal(got, want) {
			t.Fatalf("unexpected file mutation: %v", errRead)
		}
	}
	for _, url := range []string{"/v8/management/config", "/v8/management/config.yaml", "/v0/management/config.yaml"} {
		request(http.MethodGet, url, "", http.StatusOK)
		assertUnchanged([]byte(raw))
	}
	for _, invalid := range []string{
		`{"api-keys":{"codex":[{"name":"invalid","base-url":"https://example.invalid","keys":[{"api-key":"synthetic","concurrency-limit":3.5}]}]}}`,
		`{"oauth":{"providers":{"codex":{"identity-confuse":true}}}}`,
		`{"oauth":{"providers":{"codex":{"credential-proxy-policy":"invalid"}}}}`,
	} {
		request(http.MethodPatch, "/v8/management/config", invalid, http.StatusUnprocessableEntity)
		assertUnchanged([]byte(raw))
		if h.cfg != cfg {
			t.Fatal("failed migration replaced runtime configuration")
		}
	}
	request(http.MethodPatch, "/v8/management/config", `{"routing":{"retry":{"request-retry":2}}}`, http.StatusOK)
	migrated, errRead := os.ReadFile(path)
	if errRead != nil || config.ValidateV8Config(migrated) != nil {
		t.Fatalf("invalid migrated configuration: %v", errRead)
	}
	flat := request(http.MethodGet, "/v0/management/config", "", http.StatusOK).Body.Bytes()
	if gjson.GetBytes(flat, "proxy-url").String() != "socks5h://127.0.0.1:1080" || gjson.GetBytes(flat, "codex-api-key.0.concurrency-limit").Int() != 3 || !gjson.GetBytes(flat, "codex.credential-identity.enabled").Bool() {
		t.Fatal("legacy JSON management contract no longer exposes effective flat values")
	}
	served := request(http.MethodGet, "/v0/management/config.yaml", "", http.StatusOK).Body.Bytes()
	if !bytes.Equal(served, migrated) {
		t.Fatal("legacy YAML GET must serve the persisted v8 document")
	}
	assertUnchanged(migrated)
	keys := append([]config.CodexKey(nil), h.cfg.CodexKey...)
	keys[0].Priority = 7
	body, errMarshal := json.Marshal(keys)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	request(http.MethodPut, "/v0/management/codex-api-key", string(body), http.StatusOK)
	updated, _ := os.ReadFile(path)
	if errValidate := config.ValidateV8Config(updated); errValidate != nil {
		t.Fatalf("v0 provider edit restored obsolete field spellings: %v", errValidate)
	}
	reloaded, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if reloaded.CodexKey[0].Priority != 7 || reloaded.CodexKey[0].ProxyURL != "direct" || reloaded.CodexKey[0].ConcurrencyLimit == nil || *reloaded.CodexKey[0].ConcurrencyLimit != 3 || !reloaded.Codex.CredentialIdentity.Enabled || reloaded.Codex.CredentialProxyPolicy != "require" || reloaded.ProxyURL != cfg.ProxyURL {
		t.Fatal("v0 provider edit lost fork proxy, limit or identity settings")
	}
	pluginConfig := reloaded.Plugins.Configs["cpa-access-guard"]
	var rawPlugin map[string]any
	if errDecode := pluginConfig.Raw.Decode(&rawPlugin); errDecode != nil {
		t.Fatal(errDecode)
	}
	plugin, errMarshal := json.Marshal(rawPlugin)
	if errMarshal != nil || !gjson.GetBytes(plugin, "options.image-billing.price").Exists() || gjson.GetBytes(plugin, "options.image-billing.enabled").Bool() {
		t.Fatalf("plugin-owned billing policy changed: %v", errMarshal)
	}
}
