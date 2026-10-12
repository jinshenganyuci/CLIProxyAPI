package responses

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

func TestUpgrade823ClaudeRegistryDefaultsPreserveExplicitAndFableLimits(t *testing.T) {
	models := []*registry.ModelInfo{
		{ID: "upgrade823-large", MaxCompletionTokens: 96000},
		{ID: "upgrade823-small", MaxCompletionTokens: 4096},
		{ID: "upgrade823-zero", MaxCompletionTokens: 0},
		{ID: "upgrade823-fable-large", MaxCompletionTokens: 96000},
		{ID: "upgrade823-fable-small", MaxCompletionTokens: 4096},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(t.Name(), "claude", models)
	t.Cleanup(func() { reg.UnregisterClient(t.Name()) })
	for _, tc := range []struct {
		model, field string
		want         int64
	}{
		{"upgrade823-large", "", 96000},
		{"upgrade823-large", `,"max_output_tokens":null`, 96000},
		{"upgrade823-large", `,"max_output_tokens":1234`, 1234},
		{"upgrade823-large", `,"max_output_tokens":120000`, 96000},
		{"upgrade823-small", "", 4096},
		{"upgrade823-zero", "", 32000},
		{"upgrade823-unregistered", "", 32000},
		{"upgrade823-fable-large", "", 64000},
		{"upgrade823-fable-large", `,"max_output_tokens":90000`, 90000},
		{"upgrade823-fable-small", "", 4096},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/stream=%t", tc.model, tc.field, stream), func(t *testing.T) {
				// Routing has resolved the model argument; a client alias in the
				// original request must not override the selected model's limit.
				raw := []byte(`{"model":"client-visible-alias","input":"hello"` + tc.field + `}`)
				out, errConvert := ConvertOpenAIResponsesRequestToClaude(tc.model, raw, stream)
				if errConvert != nil {
					t.Fatal(errConvert)
				}
				if got := gjson.GetBytes(out, "max_tokens").Int(); got != tc.want {
					t.Fatalf("max_tokens = %d, want %d: %s", got, tc.want, out)
				}
				if got := gjson.GetBytes(out, "messages.0.content").String(); got != "hello" {
					t.Fatalf("request content changed: %s", out)
				}
			})
		}
	}
}
