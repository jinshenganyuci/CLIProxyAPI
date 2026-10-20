package management

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestV835SIWCBindPinsCredentialProxyPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, policy, credential, global, want string
		status                                 int
	}{
		{"required-missing", "require", "", "http://global.invalid:8080", "", http.StatusConflict},
		{"required-direct", "require", "direct", "http://global.invalid:8080", "direct", http.StatusOK},
		{"selected-proxy", "require", "socks5h://credential.invalid:1080", "http://global.invalid:8080", "socks5h://credential.invalid:1080", http.StatusOK},
		{"invalid-selected", "prefer", "not-a-proxy", "http://global.invalid:8080", "", http.StatusBadRequest},
		{"preferred-global", "prefer", "", "http://global.invalid:8080", "http://global.invalid:8080", http.StatusOK},
		{"preferred-direct", "prefer", "", "", "direct", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupSIWCFlow(t, true)
			f.h.cfg.Codex.CredentialProxyPolicy = tc.policy
			f.h.cfg.ProxyURL = tc.global
			f.auth.ProxyURL = tc.credential
			f.auth.Metadata["proxy_url"] = tc.credential
			if errWrite := codex.WriteJSONFileAtomic(f.path, f.auth.Metadata); errWrite != nil {
				t.Fatal(errWrite)
			}
			if _, errUpdate := f.manager.Update(context.Background(), f.auth); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			var selected *config.Config
			f.h.siwcOAuthFactory = func(cfg *config.Config) siwcOAuthService {
				selected = cfg
				return f.service
			}
			response := siwcRequest(t, f.router, http.MethodPost, "/bind", map[string]any{"name": f.auth.FileName})
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), tc.status)
			}
			if tc.status != http.StatusOK {
				if selected != nil || f.service.calls.Load() != 0 {
					t.Fatal("invalid proxy policy reached the OAuth service")
				}
				return
			}
			if selected == nil || selected.ProxyURL != tc.want {
				t.Fatal("binding did not select the credential's allowed route")
			}
			f.h.cfg.ProxyURL = "http://reloaded.invalid:8080"
			if selected.ProxyURL != tc.want {
				t.Fatal("config reload changed a pending binding route")
			}
		})
	}
}
