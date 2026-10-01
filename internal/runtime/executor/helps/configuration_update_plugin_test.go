package helps_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	helps "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type editUpdatePluginHooks struct {
	summaryRemovingPluginHooks
	effort string
}

func (h *editUpdatePluginHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	if h.effort == "" {
		out, _ := sjson.DeleteBytes(body, "input.0")
		return out
	}
	out, _ := sjson.SetBytes(body, "input.0.reasoning.effort", h.effort)
	return out
}

func TestPluginConfigurationUpdateNormalizationIsAuthoritative(t *testing.T) {
	const source = `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"hi"}]}`
	for _, tc := range []struct{ name, edit, want string }{
		{name: "delete", want: "medium"},
		{name: "rewrite", edit: "high", want: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sdktranslator.SetPluginHooks(&editUpdatePluginHooks{effort: tc.edit})
			t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
			translated := sdktranslator.TranslateRequestEnvelope(t.Context(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex, sdktranslator.RequestEnvelope{Model: "opaque-route", Body: []byte(source)})
			if !translated.ConfigurationUpdatesChanged {
				t.Fatal("plugin update edit not reported by translator")
			}
			req := cliproxyexecutor.Request{Model: "opaque-route", Payload: []byte(source), Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "opaque-route", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}}}}
			body, errApply := helps.ApplyRequestThinking(translated.Body, req, cliproxyexecutor.Options{}, "openai-response", "codex", "codex", translated.ConfigurationUpdatesChanged)
			if errApply != nil {
				t.Fatal(errApply)
			}
			if got := gjson.GetBytes(body, "reasoning.effort").String(); got != tc.want {
				t.Fatalf("effort = %q, want %q; body=%s", got, tc.want, body)
			}
			if len(gjson.GetBytes(body, "input").Array()) != 1 {
				t.Fatalf("unexpected input: %s", body)
			}
		})
	}
}

func TestNativeCrossProtocolTranslationWithoutPluginRetainsSourceUpdate(t *testing.T) {
	const source = `{"model":"private-chat","reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"hi"}]}`
	translated := sdktranslator.TranslateRequestEnvelope(t.Context(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, sdktranslator.RequestEnvelope{Model: "private-chat", Body: []byte(source)})
	if translated.ConfigurationUpdatesChanged {
		t.Fatal("native translation was incorrectly reported as a plugin edit")
	}
	req := cliproxyexecutor.Request{Model: "private-chat", Payload: []byte(source), Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "private-chat", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}}}}
	body, errApply := helps.ApplyRequestThinking(translated.Body, req, cliproxyexecutor.Options{}, "openai-response", "openai", "openai", translated.ConfigurationUpdatesChanged)
	if errApply != nil {
		t.Fatal(errApply)
	}
	if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "high" {
		t.Fatalf("cross-protocol effort = %q, want high; body=%s", got, body)
	}
}

// This pins the merge boundary between executor-aware schema normalization and
// the request-scoped authority of a plugin's configuration_update edit.
func TestV808ExecutorSchemaNormalizationPreservesPluginUpdateAuthority(t *testing.T) {
	const source = `{"model":"private-model","reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"keep user message"}],"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"number"}}}}]}`
	for _, target := range []struct{ executor, wantType string }{
		{executor: "codex", wantType: "number"},
		{executor: "codex-websockets", wantType: "number"},
		{executor: "openai-compatibility", wantType: "integer"},
		{executor: "xai", wantType: "integer"},
	} {
		for _, to := range []sdktranslator.Format{sdktranslator.FormatCodex, sdktranslator.FormatOpenAIResponse} {
			for _, edit := range []struct{ name, effort, want string }{
				{name: "delete", want: "medium"},
				{name: "rewrite", effort: "high", want: "high"},
			} {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/stream=%t", target.executor, to, edit.name, stream), func(t *testing.T) {
						sdktranslator.SetPluginHooks(&editUpdatePluginHooks{effort: edit.effort})
						t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
						sourceBytes := []byte(source)
						headers := http.Header{"User-Agent": []string{"codex_cli_rs/0.1"}}
						translated, updatesChanged := helps.TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntentForExecutor(
							t.Context(), headers, &config.Config{}, target.executor, sdktranslator.FormatOpenAIResponse, to, "private-model", sourceBytes, stream, true,
						)
						if !updatesChanged {
							t.Fatal("executor-aware translation discarded the plugin update decision")
						}
						req := cliproxyexecutor.Request{Model: "private-model", Payload: sourceBytes, Metadata: map[string]any{
							"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "private-model", IsCompat: true, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}},
						}}
						body, errApply := helps.ApplyRequestThinking(translated, req, cliproxyexecutor.Options{}, "openai-response", to.String(), "codex", updatesChanged)
						if errApply != nil {
							t.Fatal(errApply)
						}
						if got := gjson.GetBytes(body, "reasoning.effort").String(); got != edit.want {
							t.Fatalf("reasoning effort = %q, want plugin-authoritative %q; body=%s", got, edit.want, body)
						}
						if got := gjson.GetBytes(body, "tools.0.parameters.properties.yield_time_ms.type").String(); got != target.wantType {
							t.Fatalf("tool type = %q, want %q for %s", got, target.wantType, target.executor)
						}
						if input := gjson.GetBytes(body, "input").Array(); len(input) != 1 || input[0].Get("content").String() != "keep user message" {
							t.Fatalf("user content changed or configuration_update remained: %s", body)
						}
						if string(sourceBytes) != source {
							t.Fatal("source request was mutated")
						}
					})
				}
			}
		}
	}
}
