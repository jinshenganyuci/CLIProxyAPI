package live

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestValidateLiveCredentialProxyPolicy(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: config.CodexCredentialProxyPolicyRequire}}
	for _, test := range []struct {
		name     string
		selected *auth.Auth
		wantErr  bool
	}{
		{name: "missing", selected: &auth.Auth{ID: "missing"}, wantErr: true},
		{name: "invalid", selected: &auth.Auth{ID: "invalid", ProxyURL: "://bad"}, wantErr: true},
		{name: "direct", selected: &auth.Auth{ID: "direct", ProxyURL: "direct"}},
		{name: "proxy", selected: &auth.Auth{ID: "proxy", ProxyURL: "socks5://127.0.0.1:1080"}},
		{name: "api key unaffected", selected: &auth.Auth{ID: "api-key", Attributes: map[string]string{"api_key": "secret"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateLiveCredentialProxyPolicy(cfg, test.selected)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateLiveCredentialProxyPolicy() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestValidateLiveCredentialProxyPolicyPrefer(t *testing.T) {
	for _, test := range []struct {
		name, credential, global string
		apiKey, wantError        bool
	}{
		{name: "inherit"},
		{name: "inherit_global", global: "http://127.0.0.1:8080"},
		{name: "invalid_credential", credential: "invalid", global: "direct", wantError: true},
		{name: "invalid_global", global: "invalid", wantError: true},
		{name: "direct_overrides_invalid_global", credential: "direct", global: "invalid"},
		{name: "proxy_overrides_invalid_global", credential: "http://127.0.0.1:8080", global: "invalid"},
		{name: "api_key_unchanged", credential: "invalid", global: "invalid", apiKey: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: "prefer"}}
			cfg.ProxyURL = test.global
			selected := &auth.Auth{ID: "live-oauth", ProxyURL: test.credential, Metadata: map[string]any{"access_token": "synthetic-token"}}
			if test.apiKey {
				selected.Attributes = map[string]string{"api_key": "synthetic-api-key"}
			}
			errProxy := validateLiveCredentialProxyPolicy(cfg, selected)
			if (errProxy != nil) != test.wantError {
				t.Fatalf("error = %v, want error %t", errProxy, test.wantError)
			}
		})
	}
}
