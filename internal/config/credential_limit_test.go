package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIKeyConcurrencyLimitValidation(t *testing.T) {
	families := []string{"gemini-api-key", "interactions-api-key", "claude-api-key", "vertex-api-key", "codex-api-key", "xai-api-key", "meta-api-key", "openai-compatibility", "openai-compatibility-entry"}
	values := []struct {
		name  string
		raw   string
		valid bool
	}{
		{"minimum", "1", true},
		{"three", "3", true},
		{"maximum", "1000000", true},
		{"default null", "null", true},
		{"zero", "0", false},
		{"negative", "-1", false},
		{"above maximum", "1000001", false},
		{"signed overflow", "9223372036854775808", false},
		{"unsigned overflow", "18446744073709551616", false},
		{"fraction", "1.5", false},
		{"whole float", "3.0", false},
		{"exponent", "3e1", false},
		{"quoted integer", `"3"`, false},
		{"boolean", "true", false},
		{"sequence", "[3]", false},
		{"mapping", "{limit: 3}", false},
	}
	for _, family := range families {
		for _, value := range values {
			t.Run(family+"/"+value.name, func(t *testing.T) {
				payload := credentialLimitConfigYAML(family, value.raw)
				_, errParse := ParseConfigBytes([]byte(payload))
				if (errParse == nil) != value.valid {
					t.Fatalf("ParseConfigBytes() error = %v, want valid=%v", errParse, value.valid)
				}
				if !value.valid && !strings.Contains(errParse.Error(), "concurrency-limit") {
					t.Fatalf("error does not identify the invalid field: %v", errParse)
				}
				path := filepath.Join(t.TempDir(), "config.yaml")
				if errWrite := os.WriteFile(path, []byte(payload), 0600); errWrite != nil {
					t.Fatal(errWrite)
				}
				_, errLoad := LoadConfig(path)
				if (errLoad == nil) != value.valid {
					t.Fatalf("LoadConfig() error = %v, want valid=%v", errLoad, value.valid)
				}
			})
		}
	}
}

func credentialLimitConfigYAML(family, value string) string {
	if family == "openai-compatibility-entry" {
		return "openai-compatibility:\n  - name: compatibility\n    base-url: https://example.invalid/v1\n    api-key-entries:\n      - api-key: test-key\n        concurrency-limit: " + value + "\n"
	}
	return family + ":\n  - name: compatibility\n    api-key: test-key\n    base-url: https://example.invalid/v1\n    concurrency-limit: " + value + "\n"
}

func TestAPIKeyConcurrencyLimitYAMLAliasesAndMerges(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		valid   bool
	}{
		{"scalar alias", "limit: &limit 1.5\ngemini-api-key: [{api-key: key, concurrency-limit: *limit}]", false},
		{"entry alias", "entry: &entry {api-key: key, concurrency-limit: 1.5}\ngemini-api-key: [*entry]", false},
		{"sequence alias", "entries: &entries [{api-key: key, concurrency-limit: 1.5}]\ngemini-api-key: *entries", false},
		{"entry merge", "entry: &entry {concurrency-limit: 1.5}\ngemini-api-key: [{<<: *entry, api-key: key}]", false},
		{"root merge", "root: &root {gemini-api-key: [{api-key: key, concurrency-limit: 1.5}]}\n<<: *root", false},
		{"root merged sequence alias", "entries: &entries [{api-key: key, concurrency-limit: 1.5}]\nroot: &root {gemini-api-key: *entries}\n<<: *root", false},
		{"compatibility provider alias", "provider: &provider {name: compatibility, base-url: 'https://example.invalid', concurrency-limit: 1.5}\nopenai-compatibility: [*provider]", false},
		{"compatibility nested merge", "entries: &entries [{api-key: key, concurrency-limit: 1.5}]\nprovider: &provider {api-key-entries: *entries}\nopenai-compatibility: [{<<: *provider, name: compatibility, base-url: 'https://example.invalid'}]", false},
		{"valid scalar alias", "limit: &limit 3\ngemini-api-key: [{api-key: key, concurrency-limit: *limit}]", true},
		{"explicit overrides invalid merge", "entry: &entry {concurrency-limit: 1.5}\ngemini-api-key: [{<<: *entry, api-key: key, concurrency-limit: 3}]", true},
		{"null overrides invalid merge", "entry: &entry {concurrency-limit: 1.5}\ngemini-api-key: [{<<: *entry, api-key: key, concurrency-limit: null}]", true},
		{"first merge takes precedence", "good: &good {concurrency-limit: 3}\nbad: &bad {concurrency-limit: 1.5}\ngemini-api-key: [{<<: [*good, *bad], api-key: key}]", true},
		{"invalid first merge takes precedence", "good: &good {concurrency-limit: 3}\nbad: &bad {concurrency-limit: 1.5}\ngemini-api-key: [{<<: [*bad, *good], api-key: key}]", false},
		{"merge cycle", "entry: &entry {<<: *entry}\ngemini-api-key: [*entry]", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errParse := ParseConfigBytes([]byte(tc.payload))
			if (errParse == nil) != tc.valid {
				t.Fatalf("ParseConfigBytes() error = %v, want valid=%v", errParse, tc.valid)
			}
		})
	}
}

func TestCredentialConcurrencyLimitTypedValidation(t *testing.T) {
	invalid := 0
	cases := []struct {
		name string
		cfg  Config
	}{
		{"gemini-api-key", Config{GeminiKey: []GeminiKey{{ConcurrencyLimit: &invalid}}}},
		{"interactions-api-key", Config{InteractionsKey: []GeminiKey{{ConcurrencyLimit: &invalid}}}},
		{"claude-api-key", Config{ClaudeKey: []ClaudeKey{{ConcurrencyLimit: &invalid}}}},
		{"vertex-api-key", Config{VertexCompatAPIKey: []VertexCompatKey{{ConcurrencyLimit: &invalid}}}},
		{"codex-api-key", Config{CodexKey: []CodexKey{{ConcurrencyLimit: &invalid}}}},
		{"xai-api-key", Config{XAIKey: []XAIKey{{ConcurrencyLimit: &invalid}}}},
		{"meta-api-key", Config{MetaKey: []MetaKey{{ConcurrencyLimit: &invalid}}}},
		{"openai-compatibility", Config{OpenAICompatibility: []OpenAICompatibility{{ConcurrencyLimit: &invalid}}}},
		{"openai-compatibility entry", Config{OpenAICompatibility: []OpenAICompatibility{{APIKeyEntries: []OpenAICompatibilityAPIKey{{ConcurrencyLimit: &invalid}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if errValidate := tc.cfg.ValidateCredentialConcurrencyLimits(); errValidate == nil {
				t.Fatal("expected invalid credential concurrency limit")
			}
		})
	}
	if errValidate := (*Config)(nil).ValidateCredentialConcurrencyLimits(); errValidate != nil {
		t.Fatalf("nil config validation: %v", errValidate)
	}
}

func TestCredentialConcurrencyLimitPersistence(t *testing.T) {
	payload := "codex-api-key:\n  - api-key: key\n    base-url: https://example.invalid/v1\n    concurrency-limit: 3\n"
	cfg, errParse := ParseConfigBytes([]byte(payload))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if cfg.CodexKey[0].ConcurrencyLimit == nil || *cfg.CodexKey[0].ConcurrencyLimit != 3 {
		t.Fatal("explicit concurrency limit was not preserved")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(payload), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatal(errSave)
	}
	reloaded, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if reloaded.CodexKey[0].ConcurrencyLimit == nil || *reloaded.CodexKey[0].ConcurrencyLimit != 3 {
		t.Fatal("saved concurrency limit was not preserved")
	}
	cfg.CodexKey[0].ConcurrencyLimit = nil
	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatal(errSave)
	}
	reloaded, errLoad = LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if reloaded.CodexKey[0].ConcurrencyLimit != nil {
		t.Fatal("reset concurrency limit should remain omitted")
	}
}

func TestCredentialConcurrencyLimitOptionalAndJSON(t *testing.T) {
	for _, suffix := range []string{"", "\n    concurrency-limit: null"} {
		cfg, errParse := ParseConfigBytes([]byte("gemini-api-key:\n  - api-key: key" + suffix))
		if errParse != nil {
			t.Fatal(errParse)
		}
		if cfg.GeminiKey[0].ConcurrencyLimit != nil {
			t.Fatal("omitted and null limits should defer to the runtime default")
		}
		encoded, errMarshal := json.Marshal(cfg.GeminiKey[0])
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if strings.Contains(string(encoded), "concurrency-limit") {
			t.Fatalf("omitted limit leaked into JSON: %s", encoded)
		}
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(credentialLimitConfigYAML("codex-api-key", "1.5")), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	optional, errLoad := LoadConfigOptional(path, true)
	if errLoad != nil || optional == nil || len(optional.CodexKey) != 0 {
		t.Fatalf("optional invalid config should return empty standby config: cfg=%v err=%v", optional, errLoad)
	}
	limit := 3
	encoded, errMarshal := json.Marshal(CodexKey{ConcurrencyLimit: &limit})
	if errMarshal != nil || !strings.Contains(string(encoded), fmt.Sprintf(`"concurrency-limit":%d`, limit)) {
		t.Fatalf("explicit limit JSON = %s, err=%v", encoded, errMarshal)
	}
}
