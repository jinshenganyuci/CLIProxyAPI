package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestV830NewOptionsMigrationAndRuntimeSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"legacy", "tls-handshake-timeout-seconds: 7\nclaude-header-defaults: {anthropic-beta-strip: [' beta-a ', beta-a], anthropic-beta-append: [beta-b], inject-diagnostics: false, client-request-id: ' OFF '}\n"},
		{"canonical", "config-version: 8\nrequests: {tls-handshake-timeout-seconds: 7}\nupstream: {claude: {header-defaults: {anthropic-beta-strip: [' beta-a ', beta-a], anthropic-beta-append: [beta-b], inject-diagnostics: false, client-request-id: ' OFF '}}}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, []byte(tc.raw), 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			cfg, errLoad := LoadConfig(path)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			if cfg.SourceHash != ComputeSourceHash([]byte(tc.raw)) {
				t.Fatal("source hash does not describe the parsed input")
			}
			if cfg.TLSHandshakeTimeoutSeconds != 7 || cfg.ClaudeHeaderDefaults.InjectDiagnostics == nil || *cfg.ClaudeHeaderDefaults.InjectDiagnostics || cfg.ClaudeHeaderDefaults.ClientRequestID != "off" || !reflect.DeepEqual(cfg.ClaudeHeaderDefaults.AnthropicBetaStrip, []string{"beta-a"}) {
				t.Fatal("new options were not normalized")
			}
			snapshot := cfg.CloneForRuntime()
			cfg.ClaudeHeaderDefaults.AnthropicBetaStrip[0] = "mutated"
			cfg.ClaudeHeaderDefaults.AnthropicBetaAppend[0] = "mutated"
			*cfg.ClaudeHeaderDefaults.InjectDiagnostics = true
			if snapshot.ClaudeHeaderDefaults.AnthropicBetaStrip[0] != "beta-a" || snapshot.ClaudeHeaderDefaults.AnthropicBetaAppend[0] != "beta-b" || *snapshot.ClaudeHeaderDefaults.InjectDiagnostics || snapshot.SourceHash != ComputeSourceHash([]byte(tc.raw)) {
				t.Fatal("runtime snapshot shares new mutable settings or loses its source fence")
			}
			if errSave := SaveConfigPreserveComments(path, snapshot, true); errSave != nil {
				t.Fatal(errSave)
			}
			data, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if errValidate := ValidateV8Config(data); errValidate != nil {
				t.Fatal(errValidate)
			}
			reloaded, errReload := LoadConfig(path)
			if errReload != nil {
				t.Fatal(errReload)
			}
			if reloaded.TLSHandshakeTimeoutSeconds != 7 || !reflect.DeepEqual(reloaded.ClaudeHeaderDefaults, snapshot.ClaudeHeaderDefaults) {
				t.Fatal("v8 migration changed the new settings")
			}
			encoded, errJSON := json.Marshal(snapshot)
			if errJSON != nil {
				t.Fatal(errJSON)
			}
			if strings.Contains(string(data), snapshot.SourceHash) || strings.Contains(string(encoded), snapshot.SourceHash) {
				t.Fatal("runtime source hash leaked into persisted or public configuration")
			}
		})
	}
}

func TestV830NewOptionsRemainOptional(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("config-version: 8\n"))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if cfg.TLSHandshakeTimeout() != 0 || cfg.ClaudeHeaderDefaults.ClientRequestID != "" || cfg.ClaudeHeaderDefaults.InjectDiagnostics != nil || len(cfg.ClaudeHeaderDefaults.AnthropicBetaStrip) != 0 || len(cfg.ClaudeHeaderDefaults.AnthropicBetaAppend) != 0 {
		t.Fatal("omitted new settings did not retain their documented defaults")
	}
}
