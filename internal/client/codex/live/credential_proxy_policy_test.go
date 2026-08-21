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
