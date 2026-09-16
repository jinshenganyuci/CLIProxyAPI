package management

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// setConfigAPIKeyConcurrencyLimit updates the config entry that synthesized auth.
// A nil limit restores the default without changing the credential's stable ID.
func setConfigAPIKeyConcurrencyLimit(cfg *config.Config, auth *coreauth.Auth, limit *int) (bool, error) {
	if cfg == nil || auth == nil || auth.AuthSourceKind() != coreauth.AuthSourceConfig {
		return false, nil
	}
	if errValidate := config.ValidateCredentialConcurrencyLimit(limit); errValidate != nil {
		return false, errValidate
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return false, fmt.Errorf("auth id is empty")
	}

	idGen := synthesizer.NewStableIDGenerator()
	assign := func(id string, target **int) bool {
		if id != authID {
			return false
		}
		*target = nil
		if limit != nil {
			value := *limit
			*target = &value
		}
		return true
	}
	matchKey := func(kind, key, base, proxyURL, prefix string, headers map[string]string, target **int) bool {
		key = strings.TrimSpace(key)
		base = strings.TrimSpace(base)
		if key == "" && base == "" {
			return false
		}
		id, _ := idGen.Next(kind, key, base, proxyURL, prefix, config.FormatSortedHeaders(headers))
		return assign(id, target)
	}

	for i := range cfg.GeminiKey {
		entry := &cfg.GeminiKey[i]
		if matchKey("gemini:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL, entry.Prefix, entry.Headers, &entry.ConcurrencyLimit) {
			return true, nil
		}
	}
	for i := range cfg.InteractionsKey {
		entry := &cfg.InteractionsKey[i]
		if matchKey("gemini-interactions:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL, entry.Prefix, entry.Headers, &entry.ConcurrencyLimit) {
			return true, nil
		}
	}
	for i := range cfg.ClaudeKey {
		entry := &cfg.ClaudeKey[i]
		if matchKey("claude:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL, entry.Prefix, entry.Headers, &entry.ConcurrencyLimit) {
			return true, nil
		}
	}
	for i := range cfg.CodexKey {
		entry := &cfg.CodexKey[i]
		if matchKey("codex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL, entry.Prefix, entry.Headers, &entry.ConcurrencyLimit) {
			return true, nil
		}
	}
	for i := range cfg.XAIKey {
		entry := &cfg.XAIKey[i]
		if matchKey("xai:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL, entry.Prefix, entry.Headers, &entry.ConcurrencyLimit) {
			return true, nil
		}
	}
	for i := range cfg.MetaKey {
		entry := &cfg.MetaKey[i]
		if matchKey("meta:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL, entry.Prefix, entry.Headers, &entry.ConcurrencyLimit) {
			return true, nil
		}
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		providerName := strings.ToLower(strings.TrimSpace(compat.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		kind := "openai-compatibility:" + providerName
		for j := range compat.APIKeyEntries {
			entry := &compat.APIKeyEntries[j]
			id, _ := idGen.Next(kind, entry.APIKey, compat.BaseURL, entry.ProxyURL)
			if assign(id, &entry.ConcurrencyLimit) {
				return true, nil
			}
		}
		if len(compat.APIKeyEntries) == 0 {
			id, _ := idGen.Next(kind, compat.BaseURL)
			if assign(id, &compat.ConcurrencyLimit) {
				return true, nil
			}
		}
	}
	for i := range cfg.VertexCompatAPIKey {
		entry := &cfg.VertexCompatAPIKey[i]
		id, _ := idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		if assign(id, &entry.ConcurrencyLimit) {
			return true, nil
		}
	}

	return false, nil
}
