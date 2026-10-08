package helps_test

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type rejectedRequestNormalizer820 struct {
	summaryRemovingPluginHooks
	calls int
}

func (h *rejectedRequestNormalizer820) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, _ []byte, _ bool) []byte {
	h.calls++
	return []byte(`{"messages":[{"role":"user","content":"plugin replacement"}]}`)
}

func TestCompatibilityConversionFailureDoesNotInvokeNormalizerUpgrade820(t *testing.T) {
	for _, stream := range []bool{false, true} {
		hooks := &rejectedRequestNormalizer820{}
		sdktranslator.SetPluginHooks(hooks)
		t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
		payload := []byte(`{"model":"claude-sonnet-4","input":[{"role":"user","content":[{"type":"input_file","file_id":"synthetic-file"}]}]}`)
		_, errTranslate := helps.TranslateRequestReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, stream, true)
		var partErr *translatorcommon.UnsupportedPartError
		if !errors.As(errTranslate, &partErr) || partErr.Type != "input_file" {
			t.Fatalf("unsupported content lost its error: %v", errTranslate)
		}
		if hooks.calls != 0 {
			t.Fatalf("rejected input invoked normalizer %d times", hooks.calls)
		}
	}
}
