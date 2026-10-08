package translator

import (
	"testing"

	ag "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/interactions"
	gemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/interactions"
	"github.com/tidwall/gjson"
)

func TestUpgrade804InteractionsSignaturesStayWithinModelTurns(t *testing.T) {
	input := []byte(`{"input":[{"type":"thought","summary":[{"type":"text","text":"first thought"}],"signature":"thought-signature"},{"type":"function_call","call_id":"one","name":"read","arguments":{"count":9007199254740993,"enabled":false},"signature":"call-signature"},{"type":"function_result","call_id":"one","name":"read","result":{"value":null}},{"type":"user_input","content":[{"type":"text","text":"next question"}]},{"type":"function_call","call_id":"two","name":"read","arguments":{}},{"type":"function_result","call_id":"two","name":"read","result":{"value":false}},{"type":"thought","signature":"trailing-signature"}]}`)
	for _, provider := range []struct {
		name    string
		path    string
		convert func(string, []byte, bool) ([]byte, error)
	}{
		{"gemini", "contents", gemini.ConvertInteractionsRequestToGemini},
		{"antigravity", "request.contents", ag.ConvertInteractionsRequestToAntigravity},
	} {
		t.Run(provider.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				out, errConvert := provider.convert("gemini-3.8-flash-high", input, stream)
				if errConvert != nil {
					t.Fatalf("request translation failed: %v", errConvert)
				}
				signatures := map[string]int{}
				calls := map[string]gjson.Result{}
				for _, turn := range gjson.GetBytes(out, provider.path).Array() {
					for _, part := range turn.Get("parts").Array() {
						if sig := part.Get("thoughtSignature").String(); sig != "" {
							if turn.Get("role").String() != "model" {
								t.Fatalf("signature crossed into a user turn: %s", out)
							}
							signatures[sig]++
						}
						if id := part.Get("functionCall.id").String(); id != "" {
							calls[id] = part
						}
					}
				}
				for _, sig := range []string{"thought-signature", "call-signature", "trailing-signature"} {
					if signatures[sig] != 1 {
						t.Fatalf("signature %q count=%d, want 1: %s", sig, signatures[sig], out)
					}
				}
				if len(calls) != 2 || calls["one"].Get("thoughtSignature").String() != "call-signature" || calls["two"].Get("thoughtSignature").Exists() {
					t.Fatalf("pending signature leaked into a later call: %s", out)
				}
				if calls["one"].Get("functionCall.args.count").Raw != "9007199254740993" || calls["one"].Get("functionCall.args.enabled").Raw != "false" {
					t.Fatalf("signature handling changed business arguments: %s", out)
				}
			}
		})
	}
}
