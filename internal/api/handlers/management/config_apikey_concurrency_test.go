package management

import (
	"strconv"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestSetConfigAPIKeyConcurrencyLimit_SynthesizedCredentials(t *testing.T) {
	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{},
			{APIKey: " gemini-key ", BaseURL: " https://gemini.test ", ProxyURL: " socks5://proxy.test:1080 ", Prefix: " team ", Headers: map[string]string{"X-Label": "gemini"}},
			{BaseURL: "https://gemini-no-key.test"},
		},
		InteractionsKey: []config.GeminiKey{{APIKey: "interactions-key"}},
		ClaudeKey:       []config.ClaudeKey{{APIKey: "claude-key"}},
		CodexKey: []config.CodexKey{
			{APIKey: "codex-key", BaseURL: "https://codex.test", Headers: map[string]string{"X-Label": "one"}},
			{APIKey: "codex-key", BaseURL: "https://codex.test", Headers: map[string]string{"X-Label": "one"}},
			{APIKey: "codex-key", BaseURL: "https://codex.test", Headers: map[string]string{"X-Label": "two"}},
		},
		XAIKey:  []config.XAIKey{{APIKey: "xai-key"}},
		MetaKey: []config.MetaKey{{APIKey: "meta-key"}},
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: " Custom ", BaseURL: "https://compat.test", Disabled: true, APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "compat-key"}}},
			{Name: " Custom ", BaseURL: "https://compat.test", APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "compat-key"},
				{APIKey: "compat-key"},
				{ProxyURL: "socks5://proxy.test:1080"},
			}},
			{Name: "Custom", BaseURL: "https://compat-no-key.test"},
			{Name: " ", BaseURL: "https://compat-default-name.test"},
		},
		VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "vertex-key"}},
	}
	targets := []struct {
		name  string
		limit **int
	}{
		{"gemini", &cfg.GeminiKey[1].ConcurrencyLimit},
		{"gemini without key", &cfg.GeminiKey[2].ConcurrencyLimit},
		{"interactions", &cfg.InteractionsKey[0].ConcurrencyLimit},
		{"claude", &cfg.ClaudeKey[0].ConcurrencyLimit},
		{"codex", &cfg.CodexKey[0].ConcurrencyLimit},
		{"duplicate codex", &cfg.CodexKey[1].ConcurrencyLimit},
		{"codex different headers", &cfg.CodexKey[2].ConcurrencyLimit},
		{"xai", &cfg.XAIKey[0].ConcurrencyLimit},
		{"meta", &cfg.MetaKey[0].ConcurrencyLimit},
		{"compat", &cfg.OpenAICompatibility[1].APIKeyEntries[0].ConcurrencyLimit},
		{"duplicate compat", &cfg.OpenAICompatibility[1].APIKeyEntries[1].ConcurrencyLimit},
		{"compat empty key entry", &cfg.OpenAICompatibility[1].APIKeyEntries[2].ConcurrencyLimit},
		{"compat without key entries", &cfg.OpenAICompatibility[2].ConcurrencyLimit},
		{"compat default name", &cfg.OpenAICompatibility[3].ConcurrencyLimit},
		{"vertex without base URL", &cfg.VertexCompatAPIKey[0].ConcurrencyLimit},
	}
	synthesize := func() []*coreauth.Auth {
		t.Helper()
		auths, errSynthesize := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
			Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator(),
		})
		if errSynthesize != nil {
			t.Fatalf("synthesize credentials: %v", errSynthesize)
		}
		if len(auths) != len(targets) {
			t.Fatalf("synthesized %d credentials, want %d", len(auths), len(targets))
		}
		return auths
	}
	auths := synthesize()
	for i, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			limit := 3
			handled, errSet := setConfigAPIKeyConcurrencyLimit(cfg, auths[i], &limit)
			if errSet != nil || !handled {
				t.Fatalf("set limit: handled=%v err=%v", handled, errSet)
			}
			limit = 99
			if *target.limit == nil || **target.limit != 3 {
				t.Fatalf("configured limit = %v, want independent value 3", *target.limit)
			}
			for j, other := range targets {
				if j != i && *other.limit != nil {
					t.Fatalf("changed unrelated credential %s", other.name)
				}
			}
			if cfg.GeminiKey[0].ConcurrencyLimit != nil || cfg.OpenAICompatibility[0].APIKeyEntries[0].ConcurrencyLimit != nil {
				t.Fatal("changed skipped or disabled credential")
			}
			for j, after := range synthesize() {
				if after.ID != auths[j].ID {
					t.Fatalf("changed credential ID from %s to %s", auths[j].ID, after.ID)
				}
			}
			handled, errSet = setConfigAPIKeyConcurrencyLimit(cfg, auths[i], nil)
			if errSet != nil || !handled || *target.limit != nil {
				t.Fatalf("restore default: handled=%v limit=%v err=%v", handled, *target.limit, errSet)
			}
		})
	}
}

func TestSetConfigAPIKeyConcurrencyLimit_InvalidInputDoesNotMutate(t *testing.T) {
	for _, limit := range []int{-1, 0, 1_000_001} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			original := 5
			cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "key", ConcurrencyLimit: &original}}}
			id, _ := synthesizer.NewStableIDGenerator().Next("codex:apikey", "key", "", "", "", "")
			auth := &coreauth.Auth{ID: id, Attributes: map[string]string{"source": "config:codex[test]", "api_key": "key"}}
			handled, errSet := setConfigAPIKeyConcurrencyLimit(cfg, auth, &limit)
			if errSet == nil || handled {
				t.Fatalf("invalid limit accepted: handled=%v err=%v", handled, errSet)
			}
			if *cfg.CodexKey[0].ConcurrencyLimit != 5 {
				t.Fatal("invalid limit modified config")
			}
		})
	}
}

func TestSetConfigAPIKeyConcurrencyLimit_SourceAndIDGuards(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     *config.Config
		auth    *coreauth.Auth
		wantErr bool
	}{
		{name: "nil config", auth: &coreauth.Auth{ID: "id"}},
		{name: "nil auth", cfg: &config.Config{}},
		{name: "file auth", cfg: &config.Config{}, auth: &coreauth.Auth{ID: "id", Attributes: map[string]string{"source": "auths/key.json", "api_key": "key"}}},
		{name: "missing id", cfg: &config.Config{}, auth: &coreauth.Auth{Attributes: map[string]string{"source": "config:codex[test]"}}, wantErr: true},
		{name: "unknown id", cfg: &config.Config{}, auth: &coreauth.Auth{ID: "unknown", Attributes: map[string]string{"source": "config:codex[test]"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := 3
			handled, errSet := setConfigAPIKeyConcurrencyLimit(tc.cfg, tc.auth, &limit)
			if handled || (errSet != nil) != tc.wantErr {
				t.Fatalf("handled=%v err=%v, want handled=false error=%v", handled, errSet, tc.wantErr)
			}
		})
	}
}
