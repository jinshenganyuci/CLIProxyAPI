package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestConfigDiffReportsCredentialConcurrencyLimitsWithoutSecrets(t *testing.T) {
	oldConfig := &config.Config{
		GeminiKey:          []config.GeminiKey{{APIKey: "secret-gemini"}},
		InteractionsKey:    []config.GeminiKey{{APIKey: "secret-interactions"}},
		ClaudeKey:          []config.ClaudeKey{{APIKey: "secret-claude"}},
		CodexKey:           []config.CodexKey{{APIKey: "secret-codex"}},
		XAIKey:             []config.XAIKey{{APIKey: "secret-xai"}},
		MetaKey:            []config.MetaKey{{APIKey: "secret-meta"}},
		VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "secret-vertex"}},
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "compat", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "secret-compat"}}},
			{Name: "fallback"},
		},
	}
	newConfig := oldConfig.CloneForRuntime()
	limit := 3
	for _, target := range []**int{
		&newConfig.GeminiKey[0].ConcurrencyLimit, &newConfig.InteractionsKey[0].ConcurrencyLimit,
		&newConfig.ClaudeKey[0].ConcurrencyLimit, &newConfig.CodexKey[0].ConcurrencyLimit,
		&newConfig.XAIKey[0].ConcurrencyLimit, &newConfig.MetaKey[0].ConcurrencyLimit,
		&newConfig.VertexCompatAPIKey[0].ConcurrencyLimit,
		&newConfig.OpenAICompatibility[0].APIKeyEntries[0].ConcurrencyLimit,
		&newConfig.OpenAICompatibility[1].ConcurrencyLimit,
	} {
		*target = &limit
	}
	details := strings.Join(BuildConfigChangeDetails(oldConfig, newConfig), "\n")
	if strings.Count(details, "concurrency-limit") != 9 {
		t.Fatalf("missing concurrency limit changes: %s", details)
	}
	if strings.Contains(details, "secret-") {
		t.Fatal("concurrency diff exposed credential material")
	}
}
