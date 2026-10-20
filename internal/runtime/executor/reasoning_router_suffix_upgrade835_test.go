package executor

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/tidwall/gjson"
)

func TestV835CompatibleOpaqueReasoningRouterSuffix(t *testing.T) {
	buf := make([]byte, 135)
	for i := range buf {
		buf[i] = byte(i*73 + 19)
	}
	native := base64.RawStdEncoding.EncodeToString(buf)
	routed := native + ".PrivateRouter835"
	const opaque = "opaque.reasoning.token"
	if signature.DetectSignatureProvider(routed) != signature.SignatureProviderUnknown {
		t.Fatal("shape-only ciphertext must not become a provider classification")
	}
	body := []byte(fmt.Sprintf(`{"input":[{"type":"reasoning","encrypted_content":%q,"summary":[]},{"type":"reasoning","encrypted_content":%q,"summary":[]},{"type":"reasoning","encrypted_content":%q,"summary":[]},{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]},{"type":"function_call_output","call_id":"call835","output":%q}]}`, routed, native, opaque, routed, routed))
	for _, model := range []string{"gpt-5.6", "unknown-model", "claude-sonnet-4-6", "muse-wonder-1.0"} {
		for _, route := range []struct {
			name, base string
			allow      bool
		}{
			{"native", "https://api.openai.com/v1", false},
			{"router", "https://openrouter.ai/api/v1", true},
			{"router-subdomain", "https://gateway.openrouter.ai/v1", true},
			{"lookalike", "https://openrouter.ai.example.invalid/v1", false},
		} {
			t.Run(model+"/"+route.name, func(t *testing.T) {
				out := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(context.Background(), "test", body, true, model, route.base)
				if model == "muse-wonder-1.0" {
					for i := range 3 {
						if gjson.GetBytes(out, fmt.Sprintf("input.%d.encrypted_content", i)).Exists() {
							t.Fatalf("Meta target accepted an unrecognized envelope: %s", out)
						}
					}
				} else {
					want := native
					if route.allow {
						want = routed
					}
					if got := gjson.GetBytes(out, "input.0.encrypted_content").String(); got != want {
						t.Fatalf("routed reasoning = %q, want %q", got, want)
					}
					if gjson.GetBytes(out, "input.1.encrypted_content").String() != native || gjson.GetBytes(out, "input.2.encrypted_content").String() != opaque {
						t.Fatalf("unsuffixed or unrecognized opaque reasoning changed: %s", out)
					}
				}
				if gjson.GetBytes(out, "input.3.content.0.text").String() != routed || gjson.GetBytes(out, "input.4.output").String() != routed {
					t.Fatalf("literal user/tool text changed: %s", out)
				}
			})
		}
	}
	for _, value := range []string{native + ".invalid-tail", native + ".two.tails", "AAAA.PrivateRouter835"} {
		input := []byte(fmt.Sprintf(`{"input":[{"type":"reasoning","encrypted_content":%q}]}`, value))
		out := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(context.Background(), "test", input, true, "gpt-5.6", "https://api.openai.com/v1")
		if got := gjson.GetBytes(out, "input.0.encrypted_content").String(); got != value {
			t.Fatalf("unrecognized opaque value changed: got %q want %q", got, value)
		}
	}
}
