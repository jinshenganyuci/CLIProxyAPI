package config

import (
	"fmt"
	"strings"
)

const (
	// CodexCredentialProxyPolicyPrefer preserves the upstream fallback behavior.
	CodexCredentialProxyPolicyPrefer = "prefer"
	// CodexCredentialProxyPolicyRequire requires every Codex OAuth credential to
	// provide a valid per-credential proxy-url.
	CodexCredentialProxyPolicyRequire = "require"
)

// NormalizeAndValidateCodex normalizes Codex-specific configuration and rejects
// combinations that cannot provide deterministic credential identity behavior.
func (cfg *Config) NormalizeAndValidateCodex() error {
	if cfg == nil {
		return nil
	}

	policy := strings.ToLower(strings.TrimSpace(cfg.Codex.CredentialProxyPolicy))
	if policy == "" {
		policy = CodexCredentialProxyPolicyPrefer
	}
	switch policy {
	case CodexCredentialProxyPolicyPrefer, CodexCredentialProxyPolicyRequire:
		cfg.Codex.CredentialProxyPolicy = policy
	default:
		return fmt.Errorf("codex.credential-proxy-policy must be %q or %q", CodexCredentialProxyPolicyPrefer, CodexCredentialProxyPolicyRequire)
	}

	if cfg.Codex.IdentityConfuse && cfg.Codex.CredentialIdentity.Enabled {
		return fmt.Errorf("codex.identity-confuse and codex.credential-identity.enabled cannot both be true")
	}
	return nil
}
