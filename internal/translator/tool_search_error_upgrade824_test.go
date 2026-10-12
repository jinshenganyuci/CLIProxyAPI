package translator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	claude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type upgrade824RequestHooks struct {
	sdktranslator.PluginHooks
	normalized int
}

func (h *upgrade824RequestHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	h.normalized++
	return body
}

func TestUpgrade824ClaudeToolSearchEnvelopeKeepsErrorAndMetadata(t *testing.T) {
	previous := claude.ClientToolSearchEnabled()
	t.Cleanup(func() { claude.SetClientToolSearchEnabled(previous) })
	for _, pinned := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, sendable := range []bool{false, true} {
				t.Run(fmt.Sprintf("pin=%t/stream=%t/sendable=%t", pinned, stream, sendable), func(t *testing.T) {
					claude.SetClientToolSearchEnabled(!pinned)
					text := ""
					if sendable {
						text = `,{"type":"input_text","text":"retain this user turn"}`
					}
					raw := []byte(`{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file-synthetic"}` + text + `]},{"type":"tool_search_call","execution":"client","call_id":"search-1","arguments":{"query":"files"}},{"type":"tool_search_output","execution":"client","call_id":"search-1","tools":[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{}}}]},{"role":"user","content":"later text"}],"tools":[{"type":"tool_search","execution":"client"}]}`)
					before := bytes.Clone(raw)
					r := sdktranslator.NewRegistry()
					r.RegisterRequestEnvelope(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, claude.ConvertOpenAIResponsesRequestEnvelopeToClaude)
					hooks := &upgrade824RequestHooks{}
					r.SetPluginHooks(hooks)
					info := &registry.ModelInfo{ID: "claude-synthetic"}
					input := sdktranslator.RequestEnvelope{Model: info.ID, ModelInfo: info, Body: raw, Stream: stream, ConfigurationUpdatesChanged: true}
					out, errTranslate := sdktranslator.NewPipeline(r).TranslateRequest(claude.WithClientToolSearch(t.Context(), pinned), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, input)
					if out.ModelInfo != info || out.Model != input.Model || out.Stream != stream || !out.ConfigurationUpdatesChanged || !bytes.Equal(raw, before) {
						t.Fatal("envelope lost metadata or mutated original request")
					}
					if !sendable {
						var scoped *translatorcommon.UnsupportedPartError
						if !errors.As(errTranslate, &scoped) || scoped.StatusCode() != 400 || !scoped.IsRequestScoped() || !errors.Is(out.Err, errTranslate) || hooks.normalized != 0 {
							t.Fatalf("tool search rescued an unsupported user turn or error entered normalizers: err=%v envelope=%v normalized=%d", errTranslate, out.Err, hooks.normalized)
						}
						return
					}
					if errTranslate != nil || out.Err != nil || hooks.normalized != 1 {
						t.Fatalf("sendable control failed: err=%v normalized=%d", errTranslate, hooks.normalized)
					}
					if got := gjson.GetBytes(out.Body, `tools.#(name=="tool_search")`).Exists(); got != pinned {
						t.Fatalf("context pin=%t was not honored: %s", pinned, out.Body)
					}
				})
			}
		}
	}
}
