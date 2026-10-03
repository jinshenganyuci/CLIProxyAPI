package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialconcurrency"
	"gopkg.in/yaml.v3"
)

func TestV812SharedUpstreamSavePreservesForkPolicyAndPluginAliases(t *testing.T) {
	const pluginYAML = `# Keep plugin policy comments.
plugins:
  enabled: true
  configs:
    z-source:
      policy: &policy {false: false, zero: 0, nil: null, text: '', list: [], map: {}, auth_index: plugin-owned}
      shared-settings: &shared {response-steering: true, stream-bootstrap-buffering: true, stream-bootstrap-timeout: 17s, disable-codex-cloaking: true, orphan-delegation-compatibility: true, model-level-cooling: true}
    a-access-guard:
      enabled: true
      options: *policy
`
	const forkPolicy = `credential-proxy-policy: require, identity-confuse: false, credential-identity: {enabled: true, synthesize-missing-installation-id: false}`
	const legacyKeys = `openai-compatibility:
  - name: test-images
    base-url: https://example.invalid/v1
    concurrency-limit: 8
    api-key-entries:
      - {api-key: synthetic-a, proxy-url: 'socks5h://127.0.0.1:1081', concurrency-limit: 3}
      - {api-key: synthetic-b, proxy-url: direct, concurrency-limit: null}
      - {api-key: synthetic-c, proxy-url: 'http://127.0.0.1:1082'}
`
	const groupedKeys = `api-keys:
  openai-compatibility:
    - name: test-images
      base-url: https://example.invalid/v1
      concurrency-limit: 8
      keys:
        - {api-key: synthetic-a, proxy-url: 'socks5h://127.0.0.1:1081', concurrency-limit: 3}
        - {api-key: synthetic-b, proxy-url: direct, concurrency-limit: null}
        - {api-key: synthetic-c, proxy-url: 'http://127.0.0.1:1082'}
`
	for _, layout := range []string{"legacy", "historical-v8", "historical-unversioned", "canonical-v8"} {
		t.Run(layout, func(t *testing.T) {
			raw := pluginYAML
			switch layout {
			case "legacy":
				raw += "codex: {<<: *shared, optimize-multi-agent-v2: true, " + forkPolicy + "}\n" + legacyKeys
			case "historical-v8", "historical-unversioned":
				if layout == "historical-v8" {
					raw += "config-version: 8\n"
				}
				raw += "oauth: {providers: {codex: {<<: *shared, optimize-multi-agent-v2: true, " + forkPolicy + "}}}\n" + groupedKeys
			case "canonical-v8":
				raw += "config-version: 8\nupstream: {codex: *shared}\nclient: {codex: {optimize-multi-agent-v2: true, enable-apply-patch: false}}\noauth: {providers: {codex: {" + forkPolicy + "}}}\n" + groupedKeys
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			cfg, errLoad := LoadConfig(path)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			unchanged, errRead := os.ReadFile(path)
			if errRead != nil || !bytes.Equal(unchanged, []byte(raw)) {
				t.Fatalf("read-only startup rewrote configuration: %v", errRead)
			}
			var original yaml.Node
			if errDecode := yaml.Unmarshal([]byte(raw), &original); errDecode != nil {
				t.Fatal(errDecode)
			}
			var expectedPlugins any
			if errDecode := yamlPath(original.Content[0], "plugins.configs").Decode(&expectedPlugins); errDecode != nil {
				t.Fatal(errDecode)
			}
			for round := 0; round < 3; round++ {
				cfg.RequestRetry = round + 1
				if errSave := SaveConfigPreserveComments(path, cfg, round > 0); errSave != nil {
					t.Fatalf("round %d save: %v", round, errSave)
				}
				saved, errRead := os.ReadFile(path)
				if errRead != nil {
					t.Fatal(errRead)
				}
				var doc yaml.Node
				if errDecode := yaml.Unmarshal(saved, &doc); errDecode != nil {
					t.Fatal(errDecode)
				}
				canonical := layout != "legacy" || round > 0
				if canonical {
					if errValidate := ValidateV8Config(saved); errValidate != nil {
						t.Fatalf("round %d canonical validation: %v", round, errValidate)
					}
					for _, field := range []string{"upstream.codex.response-steering", "client.codex.optimize-multi-agent-v2", "oauth.providers.codex.credential-identity.enabled", "oauth.providers.codex.credential-proxy-policy"} {
						if yamlPath(doc.Content[0], field) == nil {
							t.Fatalf("round %d omitted canonical field %s", round, field)
						}
					}
					for _, alias := range v8Aliases {
						if yamlPath(doc.Content[0], alias.old) != nil {
							t.Fatalf("round %d retained historical alias %s", round, alias.old)
						}
					}
				} else if IsV8ConfigLayout(doc.Content[0]) || yamlPath(doc.Content[0], "codex.optimize-multi-agent-v2") == nil {
					t.Fatal("ordinary legacy save introduced v8 paths or lost the historical client setting")
				}
				var actualPlugins any
				if errDecode := yamlPath(doc.Content[0], "plugins.configs").Decode(&actualPlugins); errDecode != nil {
					t.Fatal(errDecode)
				}
				if !reflect.DeepEqual(actualPlugins, expectedPlugins) {
					t.Fatalf("round %d changed arbitrary plugin values", round)
				}
				if !strings.Contains(string(saved), "Keep plugin policy comments.") {
					t.Fatal("save dropped plugin policy comments")
				}
				cfg, errLoad = LoadConfig(path)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				if cfg.RequestRetry != round+1 || !cfg.Codex.CredentialIdentity.Enabled || cfg.Codex.IdentityConfuse || cfg.Codex.CredentialProxyPolicy != "require" || !cfg.Client.Codex.OptimizeMultiAgentV2 || cfg.Client.Codex.EnableApplyPatch {
					t.Fatalf("round %d changed fork/client policy", round)
				}
				for _, scoped := range []*Config{cfg, cfg.CloneForRuntime(), cfg.ForAPIKey()} {
					if !scoped.Codex.ResponseSteering || !scoped.Codex.StreamBootstrapBuffering || scoped.Codex.StreamBootstrapTimeout != "17s" || !scoped.Codex.DisableCodexCloaking || !scoped.Codex.OrphanDelegationCompatibility || !scoped.Codex.ModelLevelCooling {
						t.Fatalf("round %d dropped shared upstream settings", round)
					}
				}
				if canonical && (cfg.ForAPIKey().Codex.CredentialIdentity.Enabled || cfg.ForAPIKey().Codex.CredentialProxyPolicy != "") {
					t.Fatal("OAuth-only fork policy leaked into API-key configuration")
				}
				keys := cfg.OpenAICompatibility[0].APIKeyEntries
				if len(keys) != 3 || keys[0].ConcurrencyLimit == nil || *keys[0].ConcurrencyLimit != 3 || keys[1].ConcurrencyLimit != nil || keys[2].ConcurrencyLimit != nil || credentialconcurrency.DefaultLimit != 10 {
					t.Fatal("key concurrency or default changed, or provider fallback limit leaked into real keys")
				}
				if keys[0].ProxyURL != "socks5h://127.0.0.1:1081" || keys[1].ProxyURL != "direct" || keys[2].ProxyURL != "http://127.0.0.1:1082" {
					t.Fatal("per-key proxy policy changed")
				}
			}
		})
	}
}

func TestV812CanonicalFalseWinsWithoutDroppingForkIdentity(t *testing.T) {
	for _, value := range []string{"false", "null"} {
		t.Run(value, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`codex: {response-steering: true, optimize-multi-agent-v2: true, credential-proxy-policy: prefer}
oauth: {providers: {codex: {response-steering: true, optimize-multi-agent-v2: true, credential-proxy-policy: require, credential-identity: {enabled: true}, identity-confuse: false}}}
upstream: {codex: {response-steering: %s}}
client: {codex: {optimize-multi-agent-v2: %s}}
plugins: {configs: {cpa-access-guard: {future-policy: {upstream: false, auth_index: arbitrary}}}}
`, value, value))
			migrated, _, errNormalize := NormalizeConfigLayout(raw, true)
			if errNormalize != nil {
				t.Fatal(errNormalize)
			}
			if errValidate := ValidateV8Config(migrated); errValidate != nil {
				t.Fatal(errValidate)
			}
			cfg, errParse := ParseConfigBytes(migrated)
			if errParse != nil {
				t.Fatal(errParse)
			}
			if cfg.Codex.ResponseSteering || cfg.Client.Codex.OptimizeMultiAgentV2 || !cfg.Codex.CredentialIdentity.Enabled || cfg.Codex.CredentialProxyPolicy != "require" {
				t.Fatal("canonical zero-value precedence changed fork policy")
			}
		})
	}
}

func TestV812ExampleUsesCanonicalSharedAndForkPaths(t *testing.T) {
	raw, errRead := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errValidate := ValidateV8Config(raw); errValidate != nil {
		t.Fatalf("example is not canonical v8: %v", errValidate)
	}
	var doc yaml.Node
	if errDecode := yaml.Unmarshal(raw, &doc); errDecode != nil {
		t.Fatal(errDecode)
	}
	for _, field := range []string{"upstream.codex.response-steering", "client.codex.optimize-multi-agent-v2", "client.codex.enable-apply-patch", "oauth.providers.codex.credential-identity.enabled", "oauth.providers.codex.credential-proxy-policy", "oauth.providers.codex.identity-confuse"} {
		if yamlPath(doc.Content[0], field) == nil {
			t.Fatalf("missing example setting %s", field)
		}
	}
}
