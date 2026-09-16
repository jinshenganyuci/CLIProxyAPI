package config

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialconcurrency"
	"gopkg.in/yaml.v3"
)

// ValidateCredentialConcurrencyLimit validates an optional local credential limit.
func ValidateCredentialConcurrencyLimit(limit *int) error {
	if limit == nil {
		return nil
	}
	_, errParse := credentialconcurrency.ParseValue(*limit)
	return errParse
}

func validateCredentialConcurrencyLimitYAML(data []byte) error {
	// Decode into native values before the typed config: decoding directly into int
	// can truncate YAML floats. Native decoding also resolves aliases and merge keys
	// with YAML precedence, including aliases on the root and credential sequences.
	var document any
	if errUnmarshal := yaml.Unmarshal(data, &document); errUnmarshal != nil {
		return errUnmarshal
	}
	for _, family := range []string{
		"gemini-api-key", "interactions-api-key", "claude-api-key", "vertex-api-key",
		"codex-api-key", "xai-api-key", "meta-api-key",
	} {
		if errValidate := validateCredentialLimitSequence(yamlMappingValue(document, family), family); errValidate != nil {
			return errValidate
		}
	}
	providers, _ := yamlMappingValue(document, "openai-compatibility").([]any)
	for index, provider := range providers {
		if errValidate := validateCredentialLimitMapping(provider, fmt.Sprintf("openai-compatibility[%d]", index)); errValidate != nil {
			return errValidate
		}
		path := fmt.Sprintf("openai-compatibility[%d].api-key-entries", index)
		if errValidate := validateCredentialLimitSequence(yamlMappingValue(provider, "api-key-entries"), path); errValidate != nil {
			return errValidate
		}
	}
	return nil
}

func yamlMappingValue(mapping any, key string) any {
	switch value := mapping.(type) {
	case map[string]any:
		return value[key]
	case map[any]any:
		return value[key]
	default:
		return nil
	}
}

func validateCredentialLimitSequence(value any, path string) error {
	sequence, _ := value.([]any)
	for index, entry := range sequence {
		if errValidate := validateCredentialLimitMapping(entry, fmt.Sprintf("%s[%d]", path, index)); errValidate != nil {
			return errValidate
		}
	}
	return nil
}

func validateCredentialLimitMapping(entry any, path string) error {
	limit := yamlMappingValue(entry, "concurrency-limit")
	if limit == nil {
		return nil
	}
	switch limit.(type) {
	case int, int64, uint64:
		if _, errParse := credentialconcurrency.ParseValue(limit); errParse != nil {
			return fmt.Errorf("%s.concurrency-limit: %w", path, errParse)
		}
	default:
		return fmt.Errorf("%s.concurrency-limit: concurrency limit must be an integer", path)
	}
	return nil
}

// ValidateCredentialConcurrencyLimits validates local limits for all API-key families.
func (cfg *Config) ValidateCredentialConcurrencyLimits() error {
	if cfg == nil {
		return nil
	}
	validate := func(limit *int, family string, index int) error {
		if errValidate := ValidateCredentialConcurrencyLimit(limit); errValidate != nil {
			return fmt.Errorf("%s[%d].concurrency-limit: %w", family, index, errValidate)
		}
		return nil
	}
	for index := range cfg.GeminiKey {
		if errValidate := validate(cfg.GeminiKey[index].ConcurrencyLimit, "gemini-api-key", index); errValidate != nil {
			return errValidate
		}
	}
	for index := range cfg.InteractionsKey {
		if errValidate := validate(cfg.InteractionsKey[index].ConcurrencyLimit, "interactions-api-key", index); errValidate != nil {
			return errValidate
		}
	}
	for index := range cfg.ClaudeKey {
		if errValidate := validate(cfg.ClaudeKey[index].ConcurrencyLimit, "claude-api-key", index); errValidate != nil {
			return errValidate
		}
	}
	for index := range cfg.VertexCompatAPIKey {
		if errValidate := validate(cfg.VertexCompatAPIKey[index].ConcurrencyLimit, "vertex-api-key", index); errValidate != nil {
			return errValidate
		}
	}
	for index := range cfg.CodexKey {
		if errValidate := validate(cfg.CodexKey[index].ConcurrencyLimit, "codex-api-key", index); errValidate != nil {
			return errValidate
		}
	}
	for index := range cfg.XAIKey {
		if errValidate := validate(cfg.XAIKey[index].ConcurrencyLimit, "xai-api-key", index); errValidate != nil {
			return errValidate
		}
	}
	for index := range cfg.MetaKey {
		if errValidate := validate(cfg.MetaKey[index].ConcurrencyLimit, "meta-api-key", index); errValidate != nil {
			return errValidate
		}
	}
	for providerIndex := range cfg.OpenAICompatibility {
		if errValidate := validate(cfg.OpenAICompatibility[providerIndex].ConcurrencyLimit, "openai-compatibility", providerIndex); errValidate != nil {
			return errValidate
		}
		for keyIndex := range cfg.OpenAICompatibility[providerIndex].APIKeyEntries {
			limit := cfg.OpenAICompatibility[providerIndex].APIKeyEntries[keyIndex].ConcurrencyLimit
			path := fmt.Sprintf("openai-compatibility[%d].api-key-entries", providerIndex)
			if errValidate := validate(limit, path, keyIndex); errValidate != nil {
				return errValidate
			}
		}
	}
	return nil
}
