package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestV815CatalogChangesPreserveForkCredentialAndOpaquePluginSettings(t *testing.T) {
	const raw = `config-version: 8
models: {catalog: '', codex-catalog: '', devin-catalog: ''}
requests: {proxy-url: 'socks5h://127.0.0.1:1080'}
oauth:
  providers:
    codex: {credential-identity: {enabled: true}, credential-proxy-policy: require, identity-confuse: false}
api-keys:
  codex:
    - name: synthetic
      keys:
        - {api-key: first, proxy-url: 'http://127.0.0.1:1081', concurrency-limit: 3}
        - {api-key: second, proxy-url: direct}
        - {api-key: third, concurrency-limit: null}
plugins:
  configs:
    cpa-access-guard: {models: {catalog: [false, 0, null, '', [], {}]}, auth_index: plugin-owned}
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	before := cfg.CloneForRuntime()
	for _, sources := range []ModelCatalogs{
		{Catalog: filepath.Join(t.TempDir(), "models.json"), CodexCatalog: "https://example.invalid/codex.json", DevinCatalog: "http://127.0.0.1:1082/devin.json"},
		{CodexCatalog: filepath.Join(t.TempDir(), "codex.json")},
		{},
	} {
		cfg.Models = sources
		if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
			t.Fatal(errSave)
		}
		saved, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errValidate := ValidateV8Config(saved); errValidate != nil {
			t.Fatal(errValidate)
		}
		reloaded, errReload := LoadConfig(path)
		if errReload != nil {
			t.Fatal(errReload)
		}
		if reloaded.Models != sources || !reflect.DeepEqual(reloaded.CodexKey, before.CodexKey) || !reflect.DeepEqual(reloaded.Codex, before.Codex) || reloaded.ProxyURL != before.ProxyURL {
			t.Fatal("catalog update changed credential proxy, identity or concurrency")
		}
		for _, view := range []*Config{reloaded.CloneForRuntime(), reloaded.ForAPIKey()} {
			if view.Models != sources {
				t.Fatal("catalog sources disappeared from a runtime view")
			}
		}
		var originalNode, savedNode yaml.Node
		if errDecode := yaml.Unmarshal([]byte(raw), &originalNode); errDecode != nil {
			t.Fatal(errDecode)
		}
		if errDecode := yaml.Unmarshal(saved, &savedNode); errDecode != nil {
			t.Fatal(errDecode)
		}
		var wantPlugin, gotPlugin any
		if errDecode := yamlPath(originalNode.Content[0], "plugins.configs.cpa-access-guard").Decode(&wantPlugin); errDecode != nil {
			t.Fatal(errDecode)
		}
		if errDecode := yamlPath(savedNode.Content[0], "plugins.configs.cpa-access-guard").Decode(&gotPlugin); errDecode != nil {
			t.Fatal(errDecode)
		}
		if !reflect.DeepEqual(gotPlugin, wantPlugin) {
			t.Fatal("catalog change normalized plugin-owned models/catalog or auth_index fields")
		}
		cfg = reloaded
	}
	beforeInvalid, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, source := range []string{"relative.json", "~/catalog.json", "file:///tmp/models.json", "https:///models.json"} {
		cfg.Models.Catalog = source
		if errSave := SaveConfigPreserveComments(path, cfg); errSave == nil {
			t.Fatalf("accepted invalid catalog source %q", source)
		}
		afterInvalid, errRead := os.ReadFile(path)
		if errRead != nil || !bytes.Equal(beforeInvalid, afterInvalid) {
			t.Fatalf("invalid catalog save altered the existing file: %v", errRead)
		}
	}
}
