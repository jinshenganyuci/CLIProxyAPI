package translator

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

func TestUpgrade820NativeRequestErrorStopsNormalizationAndReachesPipeline(t *testing.T) {
	for _, envelopeTransform := range []bool{false, true} {
		name := "function transform"
		if envelopeTransform {
			name = "envelope transform"
		}
		t.Run(name, func(t *testing.T) {
			r := NewRegistry()
			refusal := &translatorcommon.UnsupportedPartError{Type: "input_file"}
			partial := []byte(`{"contents":[],"generationConfig":{"thinkingConfig":{"includeThoughts":false}}}`)
			if envelopeTransform {
				r.RegisterRequestEnvelope(FormatOpenAIResponse, FormatGemini, func(_ context.Context, req RequestEnvelope) RequestEnvelope {
					req.Body = bytes.Clone(partial)
					req.Err = refusal
					return req
				})
			} else {
				r.Register(FormatOpenAIResponse, FormatGemini, func(string, []byte, bool) ([]byte, error) { return bytes.Clone(partial), refusal }, ResponseTransform{})
			}
			hooks := &fakePluginHooks{normalizeRequest: func([]byte) []byte { return []byte(`{"plugin_recovered":true}`) }}
			r.SetPluginHooks(hooks)
			modelInfo := &registry.ModelInfo{ID: "gemini-test"}
			input := RequestEnvelope{Format: FormatOpenAIResponse, Model: "gemini-test", Stream: true, Body: []byte(`{"reasoning":{"summary":"detailed"},"input":[{"role":"user","content":[{"type":"input_file","file_id":"file-fixture"}]}]}`), ModelInfo: modelInfo, ConfigurationUpdatesChanged: true}
			pipeline := NewPipeline(r)
			var middlewareErr error
			pipeline.UseRequest(func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
				out, err := next(ctx, req)
				middlewareErr = err
				return out, err
			})
			out, err := pipeline.TranslateRequest(t.Context(), FormatOpenAIResponse, FormatGemini, input)
			if !errors.Is(err, refusal) || !errors.Is(middlewareErr, refusal) || !errors.Is(out.Err, refusal) {
				t.Fatalf("native rejection was hidden: return=%v middleware=%v envelope=%v", err, middlewareErr, out.Err)
			}
			if len(hooks.calls) != 0 || !bytes.Equal(out.Body, partial) {
				t.Fatalf("failed native payload continued into summary/plugin normalization: calls=%v body=%s", hooks.calls, out.Body)
			}
			if out.ModelInfo != modelInfo || out.Model != input.Model || !out.Stream || !out.ConfigurationUpdatesChanged {
				t.Fatal("failure lost request-scoped metadata")
			}
			var scoped interface {
				StatusCode() int
				IsRequestScoped() bool
			}
			if !errors.As(err, &scoped) || scoped.StatusCode() != 400 || !scoped.IsRequestScoped() {
				t.Fatal("unsupported-part error lost its request scope")
			}
		})
	}
}

func TestUpgrade820PreexistingRequestErrorCannotBeClearedByNextTransform(t *testing.T) {
	r := NewRegistry()
	calls := 0
	r.Register(FormatOpenAIResponse, FormatGemini, func(string, []byte, bool) ([]byte, error) { calls++; return []byte(`{"recovered":true}`), nil }, ResponseTransform{})
	refusal := errors.New("earlier request stage rejected")
	input := RequestEnvelope{Model: "model", Body: []byte(`{"original":true}`), Err: refusal}
	out, err := NewPipeline(r).TranslateRequest(t.Context(), FormatOpenAIResponse, FormatGemini, input)
	if calls != 0 || !errors.Is(err, refusal) || !errors.Is(out.Err, refusal) || !bytes.Equal(out.Body, input.Body) {
		t.Fatalf("next transform cleared a prior rejection: calls=%d err=%v envelope=%v body=%s", calls, err, out.Err, out.Body)
	}
}
