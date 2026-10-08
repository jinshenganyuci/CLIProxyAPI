package translator

import (
	"context"
	"testing"

	"github.com/tidwall/sjson"
)

func TestRegistryUpgrade7318TracksPluginConfigurationOwnership(t *testing.T) {
	const input = `{"model":"test","input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"hello"}]}`
	for _, native := range []bool{false, true} {
		name := "plugin route"
		if native {
			name = "native route"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				normalize func([]byte) []byte
				changed   bool
			}{
				{"unchanged update", func(body []byte) []byte { return body }, false},
				{"unrelated user edit", func(body []byte) []byte {
					out, _ := sjson.SetBytes(body, "input.1.content", "operator text")
					return out
				}, false},
				{"remove update", func(body []byte) []byte {
					out, _ := sjson.DeleteBytes(body, "input.0")
					return out
				}, true},
				{"rewrite update", func(body []byte) []byte {
					out, _ := sjson.SetBytes(body, "input.0.reasoning.effort", "high")
					return out
				}, true},
				{"append update", func(body []byte) []byte {
					out, _ := sjson.SetRawBytes(body, "input.-1", []byte(`{"type":"configuration_update","reasoning":{"effort":"high"}}`))
					return out
				}, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					registry := NewRegistry()
					if native {
						registry.Register(FormatOpenAIResponse, FormatCodex, func(_ string, body []byte, _ bool) ([]byte, error) {
							return body, nil
						}, ResponseTransform{})
					}
					registry.SetPluginHooks(&fakePluginHooks{normalizeRequest: tc.normalize})
					out, err := NewPipeline(registry).TranslateRequest(context.Background(), FormatOpenAIResponse, FormatCodex, RequestEnvelope{
						Format: FormatOpenAIResponse,
						Model:  "test",
						Body:   []byte(input),
					})
					if err != nil {
						t.Fatal(err)
					}
					if out.ConfigurationUpdatesChanged != tc.changed {
						t.Fatalf("plugin update ownership=%v, want %v; body=%s", out.ConfigurationUpdatesChanged, tc.changed, out.Body)
					}
					if out.Format != FormatCodex {
						t.Fatalf("output format=%q, want codex", out.Format)
					}
				})
			}
		})
	}
}

func TestRegistryUpgrade7318NativeTranslationDoesNotClaimPluginUpdateOwnership(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAIResponse, FormatClaude, func(_ string, _ []byte, _ bool) ([]byte, error) {
		return []byte(`{"messages":[{"role":"user","content":"hello"}]}`), nil
	}, ResponseTransform{})
	registry.SetPluginHooks(&fakePluginHooks{})
	for _, alreadyChanged := range []bool{false, true} {
		out := registry.TranslateRequestEnvelope(context.Background(), FormatOpenAIResponse, FormatClaude, RequestEnvelope{
			Model:                       "test",
			Body:                        []byte(`{"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"hello"}]}`),
			ConfigurationUpdatesChanged: alreadyChanged,
		})
		if out.ConfigurationUpdatesChanged != alreadyChanged {
			t.Fatalf("native cross-protocol conversion changed plugin ownership from %v to %v", alreadyChanged, out.ConfigurationUpdatesChanged)
		}
	}
}
