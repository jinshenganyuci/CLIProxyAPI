package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestUpgrade802ChatServiceTierSurvivesTranslationAndThinking(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{`"fast"`, "priority"},
		{`" PRIORITY "`, "priority"},
		{`"ultrafast"`, "ultrafast"},
		{`"auto"`, ""},
		{`"default"`, ""},
		{`null`, ""},
		{`{"name":"priority"}`, ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			source := []byte(`{"messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`)
			source, _ = sjson.SetRawBytes(source, "service_tier", []byte(tc.input))
			for _, stream := range []bool{false, true} {
				baseline, working := TranslateRequestPairWithCodexMultiAgentV2(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAI, sdktranslator.FormatCodex, "gpt-6-sol", source, source, stream)
				out, err := ApplyThinkingWithSourcePayload(working, source, source, "gpt-6-sol", "openai", "codex", "codex")
				if err != nil {
					t.Fatal(err)
				}
				for label, payload := range map[string][]byte{"baseline": baseline, "final": out} {
					tier := gjson.GetBytes(payload, "service_tier")
					if tier.String() != tc.want || tier.Exists() != (tc.want != "") {
						t.Fatalf("%s stream=%v tier=%s, want %q: %s", label, stream, tier.Raw, tc.want, payload)
					}
					if effort := gjson.GetBytes(payload, "reasoning.effort").String(); effort != "high" {
						t.Fatalf("%s effort=%q, want high", label, effort)
					}
				}
				if gjson.GetBytes(source, "service_tier").Raw != tc.input {
					t.Fatal("translation mutated the caller-owned source")
				}
			}
		})
	}
}

func TestUpgrade802AntigravitySummaryIntentSurvivesThinking(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reasoning string
		want      bool
	}{
		{"effort only", `{"effort":"low"}`, true},
		{"explicit auto", `{"effort":"low","summary":"auto"}`, true},
		{"explicit null", `{"effort":"low","summary":null}`, false},
		{"explicit none", `{"effort":"low","summary":"none"}`, false},
		{"legacy alias", `{"effort":"low","generate_summary":"detailed"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := []byte(`{"model":"gemini-3.8-flash-high","input":"hello"}`)
			source, _ = sjson.SetRawBytes(source, "reasoning", []byte(tc.reasoning))
			for _, stream := range []bool{false, true} {
				translated := TranslateRequestWithCodexMultiAgentV2(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity, "gemini-3.8-flash-high", source, stream)
				out, err := ApplyThinkingWithSourcePayload(translated, source, source, "gemini-3.8-flash-high", "openai-response", "antigravity", "antigravity")
				if err != nil {
					t.Fatal(err)
				}
				include := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts")
				if !include.Exists() || include.Bool() != tc.want {
					t.Fatalf("stream=%v includeThoughts=%s, want %v: %s", stream, include.Raw, tc.want, out)
				}
				if level := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); level != "low" {
					t.Fatalf("summary visibility changed thinking level to %q: %s", level, out)
				}
			}
		})
	}
}
