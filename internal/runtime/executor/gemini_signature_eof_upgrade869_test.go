package executor

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	geminiopenai "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/chat-completions"
)

func TestV869GeminiSignatureCarrierPreservesPartialArgsEOFValidation(t *testing.T) {
	type convertFunc func(context.Context, string, []byte, []byte, []byte, *any) [][]byte
	for _, format := range []struct {
		name    string
		convert convertFunc
	}{
		{"claude", geminiclaude.ConvertGeminiResponseToClaude},
		{"openai", geminiopenai.ConvertGeminiResponseToOpenAI},
	} {
		for _, ending := range []string{"incomplete", "complete"} {
			t.Run(format.name+"/"+ending, func(t *testing.T) {
				var param any
				var output bytes.Buffer
				feed := func(frame []byte) {
					for _, chunk := range format.convert(t.Context(), "gemini-3.1-pro-preview", nil, nil, frame, &param) {
						output.Write(chunk)
					}
				}
				feed([]byte(`{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]}}]}`))
				signature := testVertexBareTinkSignature()
				carrier := ojson.NewDocString(`{"candidates":[{"index":0,"content":{"parts":[{"thoughtSignature":""}]}}]}`)
				_ = carrier.Set("candidates.0.content.parts.0.thoughtSignature", signature)
				feed(carrier.Bytes())
				if ending == "complete" {
					feed([]byte(`{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"York"}],"willContinue":false}}]},"finishReason":"STOP"}]}`))
				}
				state, ok := param.(interface {
					FinalizeToolInput() [][]byte
					ToolInputError() error
				})
				if !ok {
					t.Fatalf("signature state lost tool EOF validation: %T", param)
				}
				if chunks := state.FinalizeToolInput(); len(chunks) != 0 {
					t.Fatalf("EOF fabricated payload chunks: %q", chunks)
				}
				if ending == "incomplete" {
					if !errors.Is(state.ToolInputError(), translatorcommon.ErrVertexPartialArgsIncomplete) {
						t.Fatalf("signature carrier concealed incomplete arguments: %v", state.ToolInputError())
					}
					if bytes.Contains(output.Bytes(), []byte(`"message_stop"`)) || bytes.Contains(output.Bytes(), []byte(`"finish_reason":"tool_calls"`)) {
						t.Fatalf("incomplete signed call emitted success: %s", output.Bytes())
					}
					return
				}
				if state.ToolInputError() != nil {
					t.Fatalf("complete signed call rejected: %v", state.ToolInputError())
				}
				marker := signature
				if format.name == "claude" {
					marker = "cpa-gemini-carrier-v1:previous:function:"
				}
				if !bytes.Contains(output.Bytes(), []byte(marker)) {
					t.Fatalf("completed call lost detached signature: %s", output.Bytes())
				}
			})
		}
	}
}
