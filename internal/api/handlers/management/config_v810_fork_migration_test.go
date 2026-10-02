package management

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func TestV810ClientMigrationPreservesForkCredentialAndPluginSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const baseline = `# Keep deployment settings.
config-version: 8
requests: {proxy-url: socks5h://127.0.0.1:1080}
oauth:
  providers:
    codex:
      credential-identity: {enabled: true}
      credential-proxy-policy: require
api-keys:
  codex:
    - name: synthetic
      base-url: https://example.invalid/v1
      keys:
        - {api-key: synthetic-a, proxy-url: direct, concurrency-limit: 3}
        - {api-key: synthetic-b, proxy-url: socks5h://127.0.0.1:1081}
plugins:
  configs:
    cpa-access-guard:
      enabled: true
      options: {price: 0, flag: false, absent: null, empty: {}, list: [3, 2, 10]}
`
	for _, tc := range []struct {
		name, oldPath, canonical string
		want                     bool
	}{
		{"flat", "codex", "", true},
		{"providers", "providers.codex", "", true},
		{"oauth", "oauth.providers.codex", "", true},
		{"explicit false wins", "oauth.providers.codex", "false", false},
		{"explicit null wins", "oauth.providers.codex", "null", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var document yaml.Node
			if errDecode := yaml.Unmarshal([]byte(baseline), &document); errDecode != nil {
				t.Fatal(errDecode)
			}
			put := func(path, literal string) {
				t.Helper()
				node := document.Content[0]
				for _, part := range strings.Split(path, ".") {
					next := configV8Node(node, []string{part})
					if next == nil {
						next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
						node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, next)
					}
					node = next
				}
				var value yaml.Node
				if errDecode := yaml.Unmarshal([]byte(literal), &value); errDecode != nil {
					t.Fatal(errDecode)
				}
				*node = *value.Content[0]
			}
			put(tc.oldPath+".optimize-multi-agent-v2", "true")
			if tc.canonical != "" {
				put("client.codex.optimize-multi-agent-v2", tc.canonical)
			}
			raw, errEncode := yaml.Marshal(&document)
			if errEncode != nil {
				t.Fatal(errEncode)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, raw, 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			cfg, errLoad := config.LoadConfig(path)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			read := func() []byte {
				t.Helper()
				data, errRead := os.ReadFile(path)
				if errRead != nil {
					t.Fatal(errRead)
				}
				return data
			}
			// Loading mixed layouts can remove conflicting historical aliases.
			// Operational GET/rejected writes must preserve the resulting file.
			before := read()
			beforeInfo, errStat := os.Stat(path)
			if errStat != nil {
				t.Fatal(errStat)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			router := gin.New()
			router.GET("/v8/management/config/*path", h.ConfigV8)
			router.PUT("/v8/management/config/*path", h.ConfigV8)
			router.DELETE("/v8/management/config/*path", h.ConfigV8)
			router.PATCH("/v8/management/config", h.ConfigV8)
			request := func(method, endpoint, body string, status int) string {
				t.Helper()
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(method, endpoint, strings.NewReader(body)))
				if response.Code != status {
					t.Fatalf("%s %s: status=%d body=%s", method, endpoint, response.Code, response.Body.String())
				}
				return strings.TrimSpace(response.Body.String())
			}
			const client = "/v8/management/config/client/codex/"
			if got := request(http.MethodGet, client+"optimize-multi-agent-v2", "", http.StatusOK); got != fmt.Sprint(tc.want) && !(tc.canonical == "null" && got == "null") {
				t.Fatalf("client view did not honor historical/canonical precedence: %s", got)
			}
			request(http.MethodPut, client+"enable-apply-patch", `"invalid"`, http.StatusUnprocessableEntity)
			request(http.MethodPut, "/v8/management/config/"+strings.ReplaceAll(tc.oldPath, ".", "/")+"/optimize-multi-agent-v2", "true", http.StatusBadRequest)
			if !bytes.Equal(read(), before) {
				t.Fatal("read or rejected client mutation rewrote the file")
			}
			assertSettings := func(applyPatch bool) {
				t.Helper()
				reloaded, errReload := config.LoadConfig(path)
				if errReload != nil {
					t.Fatal(errReload)
				}
				for _, current := range []*config.Config{h.cfg, reloaded} {
					if current.Client.Codex.OptimizeMultiAgentV2 != tc.want || current.Client.Codex.EnableApplyPatch != applyPatch || current.ForAPIKey().Client != current.Client {
						t.Fatal("client settings were lost or incorrectly restricted to OAuth")
					}
					if current.ProxyURL != cfg.ProxyURL || !reflect.DeepEqual(current.Codex, cfg.Codex) || !reflect.DeepEqual(current.CodexKey, cfg.CodexKey) {
						t.Fatal("client migration changed credential proxy, identity or concurrency")
					}
					var gotPlugin, wantPlugin any
					currentPlugin := current.Plugins.Configs["cpa-access-guard"]
					originalPlugin := cfg.Plugins.Configs["cpa-access-guard"]
					if errDecode := currentPlugin.Raw.Decode(&gotPlugin); errDecode != nil {
						t.Fatal(errDecode)
					}
					if errDecode := originalPlugin.Raw.Decode(&wantPlugin); errDecode != nil {
						t.Fatal(errDecode)
					}
					if !reflect.DeepEqual(gotPlugin, wantPlugin) {
						t.Fatal("client migration changed opaque plugin values")
					}
				}
				if errValidate := config.ValidateV8Config(read()); errValidate != nil {
					t.Fatal(errValidate)
				}
			}
			request(http.MethodPatch, "/v8/management/config", `{"requests":{"nonstream-keepalive-interval":1}}`, http.StatusOK)
			assertSettings(false)
			request(http.MethodPut, client+"enable-apply-patch", "true", http.StatusOK)
			assertSettings(true)
			request(http.MethodDelete, client+"enable-apply-patch", "", http.StatusOK)
			assertSettings(false)
			afterInfo, errStat := os.Stat(path)
			if errStat != nil || !os.SameFile(beforeInfo, afterInfo) || !bytes.Contains(read(), []byte("# Keep deployment settings.")) {
				t.Fatalf("client writes changed file identity or lost comments: %v", errStat)
			}
		})
	}
}
