package management

import (
	"bytes"
	"encoding/json"
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

func TestV824ToolSearchConfigRoundTripPreservesForkPolicies(t *testing.T) {
	const raw = `config-version: 8
upstream: {claude: {tool-search: true}}
oauth: {providers: {codex: {credential-identity: {enabled: true}, credential-proxy-policy: require}}}
api-keys:
  claude:
    - name: synthetic
      base-url: https://synthetic.invalid
      keys:
        - {api-key: synthetic-a, native-tool-search: true, proxy-url: direct, concurrency-limit: 3}
        - {api-key: synthetic-b, native-tool-search: false, proxy-url: 'socks5h://127.0.0.1:1080', concurrency-limit: null}
        - {api-key: synthetic-c}
  vertex:
    - {name: vertex, interactions: true, keys: [{api-key: synthetic-v, interactions: false}]}
plugins: {configs: {cpa-access-guard: {enabled: true, opaque: {native-tool-search: false, tool-search: null, values: []}}}}
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	before := cfg.CloneForRuntime()
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		router.Handle(method, "/v8/management/config", h.ConfigV8)
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}
	request := func(method, suffix string, data []byte) []byte {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/v8/management/config"+suffix, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d: %s", method, suffix, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.Bytes()
	}
	root := request(http.MethodGet, "", nil)
	if !json.Valid(root) {
		t.Fatal("config GET did not return JSON")
	}
	for _, tc := range []struct {
		name, method, suffix string
		body                 []byte
		want                 bool
	}{
		{"root-roundtrip", http.MethodPut, "", root, true},
		{"explicit-false", http.MethodPut, "/upstream/claude/tool-search", []byte("false"), false},
		{"patch-enabled", http.MethodPatch, "", []byte(`{"upstream":{"claude":{"tool-search":true}}}`), true},
		{"delete-default", http.MethodDelete, "/upstream/claude/tool-search", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request(tc.method, tc.suffix, tc.body)
			loaded, errReload := config.LoadConfig(path)
			if errReload != nil {
				t.Fatal(errReload)
			}
			if loaded.Claude.ToolSearch != tc.want || !reflect.DeepEqual(loaded.ClaudeKey, before.ClaudeKey) || !reflect.DeepEqual(loaded.Codex, before.Codex) || !reflect.DeepEqual(loaded.VertexCompatAPIKey, before.VertexCompatAPIKey) {
				t.Fatal("saving tool-search changed an unrelated credential or fork policy")
			}
			data, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			var doc yaml.Node
			if errDecode := yaml.Unmarshal(data, &doc); errDecode != nil {
				t.Fatal(errDecode)
			}
			keys := configV8Node(doc.Content[0], []string{"api-keys", "claude"}).Content[0]
			keys = configV8Node(keys, []string{"keys"})
			second := configV8Node(keys.Content[1], []string{"native-tool-search"})
			third := configV8Node(keys.Content[2], []string{"native-tool-search"})
			if second == nil || second.Tag != "!!bool" || second.Value != "false" || third != nil {
				t.Fatal("explicit false and omitted native-tool-search were conflated")
			}
			plugin := loaded.Plugins.Configs["cpa-access-guard"]
			var actual map[string]any
			if errDecode := plugin.Raw.Decode(&actual); errDecode != nil {
				t.Fatal(errDecode)
			}
			if !reflect.DeepEqual(actual, map[string]any{"enabled": true, "opaque": map[string]any{"native-tool-search": false, "tool-search": nil, "values": []any{}}}) {
				t.Fatal("provider field normalization changed opaque plugin data")
			}
		})
	}
}
