package management

import (
	"bytes"
	"encoding/json"
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

func TestV823BlockStyleSavePreservesVertexOverridesAndOpaquePluginValues(t *testing.T) {
	const raw = `config-version: 8
oauth: {providers: {codex: {credential-identity: {enabled: true}, credential-proxy-policy: require}}}
server: {github-token: synthetic-global-token}
api-keys:
  vertex:
    - name: synthetic-vertex
      interactions: true
      base-url: https://vertex.example.invalid
      keys:
        - {api-key: synthetic-a, proxy-url: direct, concurrency-limit: 3}
        - {api-key: synthetic-b, proxy-url: 'socks5h://127.0.0.1:1080', interactions: false}
        - {api-key: synthetic-c, concurrency-limit: null, interactions: null}
plugins: {configs: {cpa-access-guard: {enabled: true}}}
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
	router.PATCH("/v8/management/config", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	plugin := map[string]any{
		"enabled": true,
		"opaque": map[string]any{
			"null": "null", "true": "false", "001": "001", "*alias": "*alias",
			"schema": map[string]any{"interactions": false, "auth_index": "plugin-owned"},
			"values": []any{false, float64(0), nil, "", []any{}, map[string]any{}},
		},
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		url := "/v8/management/config/plugins/configs/cpa-access-guard"
		var body any = plugin
		if method == http.MethodPatch {
			url = "/v8/management/config"
			body = map[string]any{"plugins": map[string]any{"configs": map[string]any{"cpa-access-guard": plugin}}}
		}
		encoded, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, url, bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d: %s", method, recorder.Code, recorder.Body.String())
		}
		saved, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errValidate := config.ValidateV8Config(saved); errValidate != nil {
			t.Fatal(errValidate)
		}
		if strings.Contains(string(saved), "opaque: {") || strings.Contains(string(saved), `"opaque":`) {
			t.Fatal("JSON-derived plugin collections were not normalized to block YAML")
		}
		loaded, errReload := config.LoadConfig(path)
		if errReload != nil {
			t.Fatal(errReload)
		}
		if !reflect.DeepEqual(loaded.VertexCompatAPIKey, before.VertexCompatAPIKey) || !reflect.DeepEqual(loaded.Codex, before.Codex) || loaded.GitHubToken != before.GitHubToken {
			t.Fatal("style-only save changed provider overrides or fork controls")
		}
		keys := loaded.VertexCompatAPIKey
		if len(keys) != 3 || keys[0].Interactions == nil || !*keys[0].Interactions || keys[1].Interactions == nil || *keys[1].Interactions || keys[2].Interactions == nil || !*keys[2].Interactions {
			t.Fatal("Vertex interactions true/false/null inheritance changed")
		}
		if keys[0].ConcurrencyLimit == nil || *keys[0].ConcurrencyLimit != 3 || keys[1].ConcurrencyLimit != nil || keys[2].ConcurrencyLimit != nil {
			t.Fatal("explicit or default credential concurrency changed")
		}
		item := loaded.Plugins.Configs["cpa-access-guard"]
		var actual map[string]any
		if errDecode := item.Raw.Decode(&actual); errDecode != nil {
			t.Fatal(errDecode)
		}
		actualJSON, errMarshal := json.Marshal(actual)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		wantJSON, _ := json.Marshal(plugin)
		if !bytes.Equal(actualJSON, wantJSON) {
			t.Fatal("normalizing styles changed plugin-owned types, keys or empty values")
		}
		var tree yaml.Node
		if errDecode := yaml.Unmarshal(saved, &tree); errDecode != nil {
			t.Fatal(errDecode)
		}
		assertV823BlockCollections(t, &tree)
	}
}

func assertV823BlockCollections(t *testing.T, node *yaml.Node) {
	t.Helper()
	if (node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode) && len(node.Content) > 0 && node.Style&yaml.FlowStyle != 0 {
		t.Fatal("saved nonempty collection retained flow style")
	}
	for _, child := range node.Content {
		assertV823BlockCollections(t, child)
	}
}
