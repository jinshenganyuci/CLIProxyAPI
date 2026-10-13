package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestUpgrade827SIWCBindUsesCredentialProxyPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		proxy        any
		want         string
		fail         bool
	}{
		{"selected", "require", "socks5://synthetic-user:synthetic-password@127.0.0.1:1080", "socks5://synthetic-user:synthetic-password@127.0.0.1:1080", false},
		{"direct", "require", "direct", "direct", false},
		{"missing_required", "require", nil, "", true},
		{"invalid_selected", "prefer", "unsupported://synthetic", "", true},
		{"invalid_type", "prefer", true, "", true},
		{"inherit_allowed", "prefer", nil, "http://127.0.0.1:9999", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "codex.json")
			raw, _ := json.Marshal(map[string]any{"type": "codex", "proxy_url": tc.proxy})
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{}
			cfg.ProxyURL = "http://127.0.0.1:9999"
			cfg.Codex.CredentialProxyPolicy = tc.policy
			got, err := resolveSIWCBindProxyURL(cfg, path)
			if (err != nil) != tc.fail || (!tc.fail && got != tc.want) {
				t.Fatalf("route resolution: error=%v matched=%t", err, got == tc.want)
			}
		})
	}
}

func TestUpgrade827ExplicitSIWCBindPreservesForkCredentialSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.json")
	metadata := map[string]any{"type": "codex", "access_token": "outer", "refresh_token": "outer-refresh", "proxy_url": "direct", "concurrency_limit": float64(3), "weight": float64(9), "disabled": true, "prefix": "synthetic-prefix", "codex_identity_version": float64(1), "codex_identity_namespace": codex.NewCredentialIdentityNamespace(), "plugin_note": map[string]any{"keep": true}, "use_siwc": false}
	before, _ := json.Marshal(metadata)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveSIWCToCodexCredentialFile(path, &codex.CodexSIWCTokenData{AccessToken: "siwc", RefreshToken: "siwc-refresh", ClientID: "client", DeviceID: "8f78e24f-939e-4bcf-9f02-5e5d5be2c884"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(after, &saved); err != nil {
		t.Fatal(err)
	}
	for key, want := range metadata {
		if key == "use_siwc" {
			continue
		}
		actual, _ := json.Marshal(saved[key])
		expected, _ := json.Marshal(want)
		if string(actual) != string(expected) {
			t.Errorf("bind changed preserved field %s", key)
		}
	}
	if saved["use_siwc"] != true || saved["siwc"] == nil {
		t.Fatal("explicit bind did not opt in")
	}
}
