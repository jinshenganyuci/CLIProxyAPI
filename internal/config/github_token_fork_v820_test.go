package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestV820GitHubTokenMigrationAndClearPreserveForkSettings(t *testing.T) {
	for _, input := range []struct {
		name, tokenYAML, want string
	}{
		{"legacy", "github-token: synthetic-original\n", "synthetic-original"},
		{"canonical", "server: {github-token: synthetic-original}\n", "synthetic-original"},
		{"canonical-empty-wins", "github-token: synthetic-stale\nserver: {github-token: ''}\n", ""},
	} {
		t.Run(input.name, func(t *testing.T) {
			raw := input.tokenYAML + `codex: {credential-identity: {enabled: true}, credential-proxy-policy: require}
codex-api-key: [{api-key: synthetic-upstream, proxy-url: direct, concurrency-limit: 3}]
plugins: {configs: {cpa-access-guard: {github-token: plugin-owned, options: [false, 0, null, '', [], {}]}}}
`
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			cfg, errLoad := LoadConfig(path)
			if errLoad != nil || cfg.GitHubToken != input.want {
				t.Fatalf("token load/precedence changed: %v", errLoad)
			}
			original := cfg.CloneForRuntime()
			var pluginBefore any
			plugin := cfg.Plugins.Configs["cpa-access-guard"]
			if errDecode := plugin.Raw.Decode(&pluginBefore); errDecode != nil {
				t.Fatal(errDecode)
			}
			for _, token := range []string{"synthetic-replacement", ""} {
				cfg.GitHubToken = token
				if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
					t.Fatal(errSave)
				}
				saved, errRead := os.ReadFile(path)
				if errRead != nil {
					t.Fatal(errRead)
				}
				if errValidate := ValidateV8Config(saved); errValidate != nil {
					t.Fatal(errValidate)
				}
				var doc yaml.Node
				if errDecode := yaml.Unmarshal(saved, &doc); errDecode != nil {
					t.Fatal(errDecode)
				}
				if yamlPath(doc.Content[0], "github-token") != nil || yamlPath(doc.Content[0], "server.github-token").Value != token {
					t.Fatal("save did not use the canonical token path")
				}
				cfg, errLoad = LoadConfig(path)
				if errLoad != nil || cfg.GitHubToken != token || cfg.CloneForRuntime().GitHubToken != token {
					t.Fatalf("token did not reload/clone correctly: %v", errLoad)
				}
				var pluginAfter any
				plugin = cfg.Plugins.Configs["cpa-access-guard"]
				if errDecode := plugin.Raw.Decode(&pluginAfter); errDecode != nil {
					t.Fatal(errDecode)
				}
				if !reflect.DeepEqual(pluginBefore, pluginAfter) || !reflect.DeepEqual(cfg.CodexKey, original.CodexKey) || !reflect.DeepEqual(cfg.Codex, original.Codex) {
					t.Fatal("global token save changed plugin fields or credential identity/proxy/concurrency")
				}
				encoded, errMarshal := json.Marshal(cfg)
				if errMarshal != nil || strings.Contains(string(encoded), "synthetic-replacement") {
					t.Fatalf("runtime JSON exposed the configured token: %v", errMarshal)
				}
			}
		})
	}
}
