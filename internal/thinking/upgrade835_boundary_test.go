package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

func TestUpgrade835CanonicalThinkingCrossFormatDisableAndSuffix(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"thinking.type and block_binding are literal text"}],"thinking":{"type":"adaptive","display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}},"output_config":{"effort":"max","format":{"type":"json_schema","schema":{"type":"object"}}}}`)
	source := []byte(`{"reasoning":{"effort":"none"}}`)
	for _, model := range []string{"claude-sonnet-5-5", "claude-opus-5-5"} {
		t.Run(model, func(t *testing.T) {
			info := registry.LookupModelInfo(model, "claude")
			if info == nil || info.Thinking == nil {
				t.Fatal("missing model thinking capabilities")
			}
			out, errApply := thinking.ApplyThinkingWithModelInfo(body, source, model, "openai-response", "claude", "claude", info)
			if errApply != nil {
				t.Fatal(errApply)
			}
			if gjson.GetBytes(out, "thinking.block_binding").Exists() || gjson.GetBytes(out, "thinking.budget_tokens").Exists() || !gjson.GetBytes(out, "output_config.format").Exists() || gjson.GetBytes(out, "messages.0.content").String() != gjson.GetBytes(body, "messages.0.content").String() {
				t.Fatalf("off intent retained stale fields or changed unrelated request data: %s", out)
			}
			wantEffort := "none"
			if model == "claude-sonnet-5-5" {
				if gjson.GetBytes(out, "thinking").Raw != `{"type":"between_tools"}` {
					t.Fatalf("sonnet off did not become between_tools: %s", out)
				}
			} else {
				wantEffort = "low"
				if gjson.GetBytes(out, "thinking.type").String() != "adaptive" || gjson.GetBytes(out, "thinking.display").String() != "omitted" {
					t.Fatalf("adaptive-only off intent lost hidden display: %s", out)
				}
			}
			if got := thinking.ExtractTranslatedReasoningEffort(out, "claude"); got != wantEffort {
				t.Fatalf("usage effort = %q, want %q", got, wantEffort)
			}
			overridden, errOverride := thinking.ApplyThinkingWithModelInfo(body, source, model+"(high)", "openai-response", "claude", "claude", info)
			if errOverride != nil {
				t.Fatal(errOverride)
			}
			if gjson.GetBytes(overridden, "thinking.type").String() != "adaptive" || thinking.ExtractTranslatedReasoningEffort(overridden, "claude") != "high" {
				t.Fatalf("explicit suffix lost precedence over source off intent: %s", overridden)
			}
		})
	}
}
