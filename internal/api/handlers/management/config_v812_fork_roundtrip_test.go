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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

func TestConfigV812AuthIndexRoundTripPreservesForkCredentialPolicy(t *testing.T) {
	const original = `config-version: 8
requests: {proxy-url: socks5h://127.0.0.1:18080}
client: {codex: {enable-apply-patch: false, optimize-multi-agent-v2: true}}
oauth:
  providers:
    codex:
      credential-proxy-policy: require
      credential-identity: {enabled: true, synthesize-missing-installation-id: false}
api-keys:
  codex:
    - name: fork-roundtrip
      base-url: https://upstream.invalid/v1
      headers: {auth_index: keep-group-header}
      keys:
        - api-key: synthetic-shared
          proxy-url: socks5h://127.0.0.1:18081
          concurrency-limit: 3
          headers: {auth_index: keep-key-header}
        - api-key: synthetic-shared
          proxy-url: direct
          concurrency-limit: null
        - api-key: synthetic-third
          proxy-url: http://127.0.0.1:18082
plugins:
  configs:
    cpa-access-guard:
      auth_index: keep-plugin-index
      auth-index: keep-plugin-hyphen
      native_key_bindings: []
      options: {zero: 0, off: false, empty: {}, items: [], absent: null}
`
	for _, tc := range []struct {
		name, method, path string
	}{
		{"root-put", http.MethodPut, ""},
		{"root-patch", http.MethodPatch, ""},
		{"provider-put", http.MethodPut, "/api-keys/codex"},
		{"provider-patch", http.MethodPatch, "/api-keys/codex"},
		{"providers-put", http.MethodPut, "/api-keys"},
		{"yaml-put", http.MethodPut, ".yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			auths, errSynth := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
			if errSynth != nil || len(auths) != 3 {
				t.Fatalf("synthesized %d credentials: %v", len(auths), errSynth)
			}
			manager := coreauth.NewManager(nil, nil, nil)
			indexesByProxy := make(map[string]string)
			for _, auth := range auths {
				auth.Index = "live-" + auth.EnsureIndex()
				registered, errRegister := manager.Register(t.Context(), auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				indexesByProxy[registered.ProxyURL] = registered.Index
			}
			if len(indexesByProxy) != 3 {
				t.Fatal("same-key credentials with different proxies collapsed")
			}
			liveBefore := make(map[string]*coreauth.Auth)
			for _, auth := range manager.List() {
				liveBefore[auth.ID] = auth
			}
			h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}
			router := gin.New()
			for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch} {
				for _, route := range []string{"/v8/management/config", "/v8/management/config.yaml", "/v8/management/config/*path"} {
					router.Handle(method, route, h.ConfigV8)
				}
			}
			request := func(method, route string, body []byte, want int) []byte {
				t.Helper()
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(method, "/v8/management/config"+route, bytes.NewReader(body)))
				if response.Code != want {
					t.Fatalf("%s %s = %d, want %d: %s", method, route, response.Code, want, response.Body.String())
				}
				return response.Body.Bytes()
			}
			var view map[string]any
			if errDecode := json.Unmarshal(request(http.MethodGet, "", nil, http.StatusOK), &view); errDecode != nil {
				t.Fatal(errDecode)
			}
			providerMap := view["api-keys"].(map[string]any)
			groups := providerMap["codex"].([]any)
			group := groups[0].(map[string]any)
			for _, rawKey := range group["keys"].([]any) {
				key := rawKey.(map[string]any)
				if key["auth_index"] != indexesByProxy[key["proxy-url"].(string)] {
					t.Fatalf("live credential index did not follow its own proxy identity: %#v", key)
				}
				key["auth-index"] = "forged-legacy-index"
			}
			group["auth_index"] = "forged-group-index"
			group["auth-index"] = "forged-group-legacy-index"
			// Reading the JSON view must not write ephemeral indexes onto disk.
			if disk, errRead := os.ReadFile(path); errRead != nil || string(disk) != original {
				t.Fatalf("GET changed persisted configuration: %v", errRead)
			}
			var upload any = view
			switch tc.path {
			case "/api-keys/codex":
				upload = groups
			case "/api-keys":
				upload = providerMap
			}
			var body []byte
			var errEncode error
			if tc.path == ".yaml" {
				body, errEncode = yaml.Marshal(upload)
			} else {
				body, errEncode = json.Marshal(upload)
			}
			if errEncode != nil {
				t.Fatal(errEncode)
			}
			request(tc.method, tc.path, body, http.StatusOK)
			saved, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			afterInfo, errStat := os.Stat(path)
			if errStat != nil || !os.SameFile(beforeInfo, afterInfo) {
				t.Fatalf("save replaced the mounted configuration inode: %v", errStat)
			}
			var want, got map[string]any
			if errDecode := yaml.Unmarshal([]byte(original), &want); errDecode != nil {
				t.Fatal(errDecode)
			}
			if errDecode := yaml.Unmarshal(saved, &got); errDecode != nil {
				t.Fatal(errDecode)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("roundtrip changed fork policy, opaque values or persisted an ephemeral index:\n%s", saved)
			}
			reloaded, errReload := config.LoadConfig(path)
			if errReload != nil {
				t.Fatal(errReload)
			}
			for _, value := range []*config.Config{h.cfg, reloaded} {
				if value.ProxyURL != cfg.ProxyURL || value.Codex.CredentialProxyPolicy != "require" || !value.Codex.CredentialIdentity.Enabled || value.Codex.CredentialIdentity.SynthesizeMissingInstallationID || len(value.CodexKey) != 3 {
					t.Fatal("runtime fork identity or proxy policy changed")
				}
				for i, key := range value.CodexKey {
					if key.ProxyURL != cfg.CodexKey[i].ProxyURL || (i == 0 && (key.ConcurrencyLimit == nil || *key.ConcurrencyLimit != 3)) || (i > 0 && key.ConcurrencyLimit != nil) {
						t.Fatalf("credential %d lost proxy or explicit/default concurrency", i)
					}
				}
			}
			liveAfter := make(map[string]*coreauth.Auth)
			for _, auth := range manager.List() {
				liveAfter[auth.ID] = auth
			}
			if !reflect.DeepEqual(liveBefore, liveAfter) {
				t.Fatal("configuration read/write mutated live credential records")
			}
			request(http.MethodPatch, "/api-keys/codex", []byte(`[{"base-url":"https://upstream.invalid","keys":[{"api-key":"bad","concurrency-limit":0}]}]`), http.StatusUnprocessableEntity)
			if rejected, errRead := os.ReadFile(path); errRead != nil || !bytes.Equal(saved, rejected) {
				t.Fatalf("rejected credential policy changed configuration: %v", errRead)
			}
		})
	}
}
