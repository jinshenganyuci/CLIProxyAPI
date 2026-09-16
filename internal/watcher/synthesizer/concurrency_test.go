package synthesizer

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialconcurrency"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func concurrencyTestConfig(limit *int) *config.Config {
	return &config.Config{
		GeminiKey:          []config.GeminiKey{{APIKey: "gemini", ConcurrencyLimit: limit}},
		InteractionsKey:    []config.GeminiKey{{APIKey: "interactions", ConcurrencyLimit: limit}},
		ClaudeKey:          []config.ClaudeKey{{APIKey: "claude", ConcurrencyLimit: limit}},
		CodexKey:           []config.CodexKey{{APIKey: "codex", ConcurrencyLimit: limit}},
		XAIKey:             []config.XAIKey{{APIKey: "xai", ConcurrencyLimit: limit}},
		MetaKey:            []config.MetaKey{{APIKey: "meta", ConcurrencyLimit: limit}},
		VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "vertex", ConcurrencyLimit: limit}},
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "compat", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "compat", ConcurrencyLimit: limit}}},
			{Name: "fallback", BaseURL: "http://example.invalid", ConcurrencyLimit: limit},
		},
	}
}

func TestConfigConcurrencyLimitPreservesIdentityAndResetsDefault(t *testing.T) {
	limit := 3
	var previous []*coreauth.Auth
	for _, configured := range []*int{nil, &limit, nil} {
		auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
			Config: concurrencyTestConfig(configured), Now: time.Unix(0, 0), IDGenerator: NewStableIDGenerator(),
		})
		if errSynthesize != nil || len(auths) != 9 {
			t.Fatalf("Synthesize() = %d auths, %v", len(auths), errSynthesize)
		}
		want := credentialconcurrency.DefaultLimit
		if configured != nil {
			want = *configured
		}
		for index, auth := range auths {
			if got := coreauth.EffectiveConcurrencyLimit(auth); got != want {
				t.Fatalf("auth %d limit = %d, want %d", index, got, want)
			}
			if previous != nil && (auth.ID != previous[index].ID || auth.EnsureIndex() != previous[index].EnsureIndex()) {
				t.Fatalf("concurrency edit changed credential identity for auth %d", index)
			}
			if configured == nil {
				if _, exists := auth.Attributes[credentialconcurrency.Field]; exists {
					t.Fatal("omitted concurrency limit was materialized into config auth")
				}
			}
		}
		previous = auths
	}
}

func TestAuthFileConcurrencyLimitValidationAndPluginInheritance(t *testing.T) {
	for _, plugin := range []bool{false, true} {
		for _, testCase := range []struct {
			value string
			want  int
		}{
			{"3", 3}, {`" 3 "`, 3}, {"null", 10}, {"1000000", 1000000},
			{"0", 0}, {"-1", 0}, {"1.5", 0}, {"true", 0}, {`""`, 0}, {"1000001", 0},
		} {
			t.Run(fmt.Sprintf("plugin_%t_value_%s", plugin, testCase.value), func(t *testing.T) {
				dir := t.TempDir()
				ctx := &SynthesisContext{Config: &config.Config{}, AuthDir: dir, Now: time.Unix(0, 0)}
				if plugin {
					ctx.PluginAuthParser = multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
						return []*coreauth.Auth{
							{ID: "project-a", Provider: "test", Metadata: map[string]any{credentialconcurrency.Field: 20, "project": "a"}},
							{ID: "project-b", Provider: "test", Attributes: map[string]string{credentialconcurrency.Field: "30"}, Metadata: map[string]any{"project": "b"}},
						}, true, nil
					})
				}
				raw := []byte(`{"type":"test","concurrency_limit":` + testCase.value + `}`)
				auths, errSynthesize := SynthesizeAuthFile(ctx, filepath.Join(dir, "test.json"), raw)
				if testCase.want == 0 {
					if errSynthesize == nil || len(auths) != 0 {
						t.Fatalf("accepted invalid limit: auths=%d, error=%v", len(auths), errSynthesize)
					}
					return
				}
				if errSynthesize != nil || len(auths) == 0 {
					t.Fatalf("synthesize file: %v", errSynthesize)
				}
				for _, auth := range auths {
					if got := coreauth.EffectiveConcurrencyLimit(auth); got != testCase.want {
						t.Fatalf("limit = %d, want %d", got, testCase.want)
					}
					if plugin && auth.Metadata["project"] == nil {
						t.Fatal("plugin-owned metadata was discarded")
					}
				}
			})
		}
	}
}

func TestAuthFileConcurrencyLimitAliasAndInvalidPluginValue(t *testing.T) {
	dir := t.TempDir()
	ctx := &SynthesisContext{Config: &config.Config{}, AuthDir: dir, Now: time.Unix(0, 0)}
	auths, errSynthesize := SynthesizeAuthFile(ctx, filepath.Join(dir, "test.json"), []byte(`{"type":"test","concurrency-limit":3}`))
	if errSynthesize != nil || len(auths) != 1 || coreauth.EffectiveConcurrencyLimit(auths[0]) != 3 {
		t.Fatalf("alias synthesis failed: auths=%v, error=%v", auths, errSynthesize)
	}
	ctx.PluginAuthParser = multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
		return []*coreauth.Auth{
			{ID: "invalid", Provider: "test", Metadata: map[string]any{credentialconcurrency.Field: 0}},
			{ID: "valid", Provider: "test", Metadata: map[string]any{credentialconcurrency.Field: 3}},
		}, true, nil
	})
	auths, errSynthesize = SynthesizeAuthFile(ctx, filepath.Join(dir, "test.json"), []byte(`{"type":"test"}`))
	if errSynthesize != nil || len(auths) != 1 || auths[0].ID != "valid" || coreauth.EffectiveConcurrencyLimit(auths[0]) != 3 {
		t.Fatalf("invalid plugin limit was accepted: auths=%v, error=%v", auths, errSynthesize)
	}
}
