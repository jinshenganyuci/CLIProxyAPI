package config

import (
	"strings"
	"testing"
)

func TestParseConfigBytesCodexCredentialIdentity(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
codex:
  credential-identity:
    enabled: true
    synthesize-missing-installation-id: false
  credential-proxy-policy: require
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.Codex.CredentialIdentity.Enabled {
		t.Fatal("credential identity enabled = false, want true")
	}
	if cfg.Codex.CredentialIdentity.SynthesizeMissingInstallationID {
		t.Fatal("synthesize missing installation id = true, want false")
	}
	if got := cfg.Codex.CredentialProxyPolicy; got != CodexCredentialProxyPolicyRequire {
		t.Fatalf("credential proxy policy = %q, want %q", got, CodexCredentialProxyPolicyRequire)
	}
}

func TestParseConfigBytesCodexCredentialIdentityRejectsLegacyCombination(t *testing.T) {
	_, err := ParseConfigBytes([]byte(`
codex:
  identity-confuse: true
  credential-identity:
    enabled: true
`))
	if err == nil || !strings.Contains(err.Error(), "cannot both be true") {
		t.Fatalf("ParseConfigBytes() error = %v, want mutually exclusive error", err)
	}
}

func TestParseConfigBytesCodexCredentialProxyPolicyDefaultsAndValidates(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("codex: {}\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() default error = %v", err)
	}
	if got := cfg.Codex.CredentialProxyPolicy; got != CodexCredentialProxyPolicyPrefer {
		t.Fatalf("default credential proxy policy = %q, want %q", got, CodexCredentialProxyPolicyPrefer)
	}

	_, err = ParseConfigBytes([]byte("codex:\n  credential-proxy-policy: sometimes\n"))
	if err == nil || !strings.Contains(err.Error(), "credential-proxy-policy") {
		t.Fatalf("ParseConfigBytes() invalid policy error = %v, want validation error", err)
	}
}
