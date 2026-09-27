package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAICompactCompatibilityUpdateMerge7318(t *testing.T) {
	const payload = `{"model":"grok-4.5","reasoning":{"effort":"medium","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"preserve message"}]}`
	for _, tc := range []struct {
		name, pluginEffort, want string
		compat                   bool
	}{
		{name: "normal delete", want: "medium"},
		{name: "normal rewrite", pluginEffort: "high", want: "high"},
		{name: "compat delete", want: "medium", compat: true},
		{name: "compat rewrite", pluginEffort: "high", want: "high", compat: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sdktranslator.SetPluginHooks(&updateNormalizingHooks{effort: tc.pluginEffort})
			t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
			req := cliproxyexecutor.Request{
				Model: "grok-4.5", Payload: []byte(payload),
				Metadata: map[string]any{
					"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{
						ID: "grok-4.5", IsCompat: tc.compat,
						Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
					},
				},
			}
			executor := NewXAIExecutor(&config.Config{})
			prepared, errPrepare := executor.prepareResponsesRequestTo(t.Context(), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, false, sdktranslator.FormatOpenAIResponse)
			if errPrepare != nil {
				t.Fatalf("prepare compact request: %v", errPrepare)
			}
			if got := gjson.GetBytes(prepared.body, "reasoning.effort").String(); got != tc.want {
				t.Fatalf("effort = %q, want %q; body=%s", got, tc.want, prepared.body)
			}
			if got := gjson.GetBytes(prepared.body, "reasoning.summary").String(); got != "auto" {
				t.Fatalf("summary = %q; body=%s", got, prepared.body)
			}
			input := gjson.GetBytes(prepared.body, "input").Array()
			if len(input) != 1 || input[0].Get("content").String() != "preserve message" {
				t.Fatalf("unsupported updates remain or user message changed: %s", prepared.body)
			}
			if string(req.Payload) != payload {
				t.Fatal("executor mutated source request")
			}
		})
	}
}
