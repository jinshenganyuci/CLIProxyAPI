package translator

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature/signaturetest"
	claudechat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/chat-completions"
	clauderesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/tidwall/gjson"
)

func TestUpgrade815ClaudeNativeJSONPreservesMixedBlocksAndUsage(t *testing.T) {
	qSignature := signaturetest.AntigravityCAQS()
	raw := []byte(fmt.Sprintf(`{"id":"message-original","type":"message","model":"claude-observed","role":"assistant","content":[{"type":"thinking","thinking":"plan <>&","signature":%q},{"type":"text","text":"Answer🙂","citations":[{"type":"web_search_result_location","url":"https://example.invalid/source","title":"Source","cited_text":"Answer🙂","encrypted_index":"opaque-citation"}]},{"type":"tool_use","id":"call-original","name":"read","input":{"count":9007199254740993,"enabled":false,"nested":{"value":null}}}],"stop_reason":"tool_use","usage":{"input_tokens":13,"cache_read_input_tokens":7,"cache_creation_input_tokens":3,"output_tokens":5}}`, qSignature))
	before := bytes.Clone(raw)
	sse, model := common.ClaudeMessagesJSONToSSE(raw)
	if model != "claude-observed" || !bytes.Equal(raw, before) {
		t.Fatal("native adapter lost the observed model or mutated its source")
	}
	request := []byte(`{"model":"claude-observed","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`)
	chat := claudechat.ConvertClaudeResponseToOpenAINonStream(t.Context(), "claude-observed", request, request, raw, nil)
	responses := clauderesponses.ConvertClaudeResponseToOpenAIResponsesNonStream(t.Context(), "claude-observed", request, request, raw, nil)
	for _, tc := range []struct {
		name, inputPath, outputPath, totalPath, cachedPath, callPath string
		out                                                          []byte
	}{
		{"chat", "usage.prompt_tokens", "usage.completion_tokens", "usage.total_tokens", "usage.prompt_tokens_details.cached_tokens", "choices.0.message.tool_calls.0", chat},
		{"responses", "usage.input_tokens", "usage.output_tokens", "usage.total_tokens", "usage.input_tokens_details.cached_tokens", "output.2", responses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for path, want := range map[string]int64{tc.inputPath: 23, tc.outputPath: 5, tc.totalPath: 28, tc.cachedPath: 7} {
				if gjson.GetBytes(tc.out, path).Int() != want {
					t.Fatalf("usage %s was lost or counted twice: %s", path, tc.out)
				}
			}
			call := gjson.GetBytes(tc.out, tc.callPath)
			args, id := call.Get("arguments").String(), call.Get("call_id").String()
			if tc.name == "chat" {
				args, id = call.Get("function.arguments").String(), call.Get("id").String()
			}
			parsed := gjson.Parse(args)
			if id != "call-original" || parsed.Get("count").Raw != "9007199254740993" || parsed.Get("enabled").Raw != "false" || parsed.Get("nested.value").Raw != "null" {
				t.Fatalf("native adaptation changed tool identity or arguments: %s", tc.out)
			}
		})
	}
	if gjson.GetBytes(chat, "usage.prompt_tokens_details.cached_creation_tokens").Int() != 3 {
		t.Fatalf("cache creation usage was lost: %s", chat)
	}
	if gjson.GetBytes(responses, "output.0.encrypted_content").String() != qSignature || gjson.GetBytes(responses, "output.0.summary.0.text").String() != "plan <>&" {
		t.Fatal("response translation changed the opaque reasoning carrier")
	}
	annotations := gjson.GetBytes(responses, "output.1.content.0.annotations").Array()
	if len(annotations) != 1 || annotations[0].Get("encrypted_index").String() != "opaque-citation" {
		t.Fatalf("native citations were lost or duplicated: %s", responses)
	}
	var state any
	var terminal gjson.Result
	for _, line := range bytes.Split(sse, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		for _, event := range clauderesponses.ConvertClaudeResponseToOpenAIResponses(t.Context(), "claude-observed", request, request, line, &state) {
			for _, eventLine := range bytes.Split(event, []byte("\n")) {
				payload := gjson.ParseBytes(bytes.TrimPrefix(eventLine, []byte("data: ")))
				if payload.Get("type").String() == "response.completed" {
					terminal = payload.Get("response")
				}
			}
		}
	}
	if !terminal.Exists() || terminal.Get("usage.total_tokens").Int() != 28 || terminal.Get("output.0.encrypted_content").String() != qSignature {
		t.Fatalf("streamed native response lost final reasoning or usage: %s", terminal.Raw)
	}
	// Preserving an opaque response carrier does not grant native replay rights.
	if signature.DecideSignatureCompatibility(signature.SignatureProviderClaude, qSignature, signature.SignatureBlockKindClaudeThinking).Compatible {
		t.Fatal("Google CAQS wrapper became replayable on a native Claude endpoint")
	}
	if normalized, ok := signature.CompatibleAntigravityClaudeThinkingSignature(qSignature); !ok || normalized != qSignature {
		t.Fatal("native response conversion changed the Google replay wrapper")
	}
}
