package translator

import (
	"bytes"
	"testing"

	"github.com/MoeForge/moego/ojson"
	clauderesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	codexresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/openai/responses"
	chatresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
)

func TestUpgrade853CodexCommentaryTokenLimitAndInputNormalization(t *testing.T) {
	for _, fields := range []string{`"max_output_tokens":4096,"max_tokens":8192`, `"max_completion_tokens":4096`} {
		input := []byte(`{` + fields + `,"counter":9007199254740993,"input":[{"type":"reasoning","encrypted_content":"cpa:commentary:v1","summary":[]},{"type":"reasoning","encrypted_content":"cpa:commentary:v1","summary":[{"text":"Checking."}]},{"type":"message","role":"system","content":"Keep exact values."},{"type":"function_call","call_id":"call_1","name":"inspect","arguments":" "}]}`)
		before := bytes.Clone(input)
		out, errConvert := codexresponses.ConvertOpenAIResponsesRequestToCodex("gpt-test", input, true)
		if errConvert != nil {
			t.Fatal(errConvert)
		}
		root := ojson.ParseBytes(out)
		if root.Get("max_output_tokens").Int() != 4096 || root.Get("max_tokens").Exists() || root.Get("max_completion_tokens").Exists() || root.Get("counter").Raw != "9007199254740993" {
			t.Fatalf("token limit normalization changed unrelated data: %s", out)
		}
		if root.Get("input.#").Int() != 3 || root.Get("input.0.phase").String() != "commentary" || root.Get("input.1.role").String() != "developer" || root.Get("input.2.arguments").String() != "{}" {
			t.Fatalf("later normalization used stale input indexes: %s", out)
		}
		if !bytes.Equal(input, before) {
			t.Fatal("translation changed the caller input")
		}
	}
}

func TestUpgrade853ResponsesEchoPreservesExactNestedNumbers(t *testing.T) {
	request := []byte(`{"model":"test","metadata":{"large":900719925474099312345,"decimal":0.123456789012345678901,"nested":[{"value":9007199254740993}]},"tools":[{"type":"function","name":"inspect","parameters":{"type":"object","properties":{"count":{"type":"integer","default":9007199254740993}}}}]}`)
	for _, provider := range []string{"openai", "claude"} {
		t.Run(provider, func(t *testing.T) {
			var state any
			var out []byte
			if provider == "openai" {
				out = chatresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(t.Context(), "test", request, request, []byte(`{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`), &state)
			} else {
				out = clauderesponses.ConvertClaudeResponseToOpenAIResponsesNonStream(t.Context(), "test", request, request, []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), &state)
			}
			for path, want := range map[string]string{
				"metadata.large":                              "900719925474099312345",
				"metadata.decimal":                            "0.123456789012345678901",
				"metadata.nested.0.value":                     "9007199254740993",
				"tools.0.parameters.properties.count.default": "9007199254740993",
			} {
				if got := ojson.GetBytes(out, path).Raw; got != want {
					t.Fatalf("%s = %s, want exact %s; response=%s", path, got, want, out)
				}
			}
		})
	}
}
