package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSaveConfigPluginUpgradeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"nested values and credential settings", `# Keep this server comment.
port: 8317
proxy-url: socks5://127.0.0.1:1080
unknown-legacy: {keep: true}
codex-api-key:
  - api-key: synthetic-key
    proxy-url: http://127.0.0.1:3128
    concurrency-limit: 3
plugins:
  enabled: true
  configs:
    sample:
      # Keep this plugin comment.
      enabled: false
      priority: 0
      options: {nil: null, bool: false, number: 0, text: "", list: [], map: {}}
      ordered: [false, 0, "", null, [], {}]
`},
		{"internal aliases", `plugins:
  enabled: true
  configs:
    sample:
      defaults: &defaults {enabled: false, number: 0, empty: [], nil: null}
      alias: *defaults
      merged: {<<: *defaults, number: 3}
`},
		{"external anchor", `defaults: &defaults {number: 0, empty: [], nil: null}
plugins:
  enabled: true
  configs:
    sample:
      alias: *defaults
`},
		{"cross plugin aliases reordered by map encoding", `plugins:
  enabled: true
  configs:
    z-provider:
      options: &shared {number: 0, empty: [], nil: null}
    a-consumer:
      options: *shared
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			var cfg Config
			if err := yaml.Unmarshal([]byte(tt.yaml), &cfg); err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := yaml.Unmarshal([]byte(tt.yaml), &want); err != nil {
				t.Fatal(err)
			}
			before, err := yaml.Marshal(&cfg)
			if err != nil {
				t.Fatal(err)
			}
			for round := 0; round < 2; round++ {
				if err := SaveConfigPreserveComments(path, &cfg); err != nil {
					t.Fatalf("save round %d: %v", round, err)
				}
				after, err := yaml.Marshal(&cfg)
				if err != nil || string(before) != string(after) {
					t.Fatalf("save mutated caller config: %v", err)
				}
				saved, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]any
				if err := yaml.Unmarshal(saved, &got); err != nil {
					t.Fatalf("decode round %d: %v\n%s", round, err, saved)
				}
				wantPlugins := want["plugins"].(map[string]any)["configs"]
				gotPlugins := got["plugins"].(map[string]any)["configs"]
				if !reflect.DeepEqual(gotPlugins, wantPlugins) {
					t.Fatalf("plugin values changed: got %#v; want %#v", gotPlugins, wantPlugins)
				}
				for _, key := range []string{"proxy-url", "codex-api-key", "unknown-legacy", "defaults"} {
					if !reflect.DeepEqual(got[key], want[key]) {
						t.Fatalf("unrelated %s changed: got %#v; want %#v", key, got[key], want[key])
					}
				}
				for _, comment := range []string{"# Keep this server comment.", "# Keep this plugin comment."} {
					if strings.Contains(tt.yaml, comment) && !strings.Contains(string(saved), comment) {
						t.Fatalf("lost comment %s", comment)
					}
				}
				if err := yaml.Unmarshal(saved, &cfg); err != nil {
					t.Fatal(err)
				}
				before, err = yaml.Marshal(&cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSaveConfigPluginUpgradeInvalidAliasLeavesFileUnchanged(t *testing.T) {
	const original = "port: 8317\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Plugins: PluginsConfig{Configs: map[string]PluginInstanceConfig{
		"sample": {Raw: yaml.Node{Kind: yaml.AliasNode, Value: "missing"}},
	}}}
	if err := SaveConfigPreserveComments(path, cfg); err == nil {
		t.Fatal("expected an unresolved alias error")
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != original {
		t.Fatalf("failed save changed the file: %s", saved)
	}
}

func TestSaveConfigPluginUpgradeDeletingAnchorPreservesExternalAlias(t *testing.T) {
	const initial = `plugins:
  enabled: true
  configs:
    sample:
      options: &options {number: 3}
unknown-legacy: *options
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(initial), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Plugins.Configs = map[string]PluginInstanceConfig{}
	if err := SaveConfigPreserveComments(path, &cfg); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(saved, &got); err != nil {
		t.Fatalf("saved config is not valid YAML: %v\n%s", err, saved)
	}
	if !reflect.DeepEqual(got["unknown-legacy"], map[string]any{"number": 3}) {
		t.Fatalf("external alias changed: %#v", got["unknown-legacy"])
	}
	if _, exists := got["plugins"].(map[string]any)["configs"]; exists {
		t.Fatal("deleted plugin configs remain")
	}
}
