package translator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	clauderesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/claude"
	codexchat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/openai/chat-completions"
	codexresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/openai/responses"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	"github.com/tidwall/gjson"
)

func TestUpgrade842OpenAndClosedToolArgumentsRoundTrip(t *testing.T) {
	original := []byte(`{"tools":[{"name":"Open","input_schema":{"type":"object","properties":{}}},{"name":"Closed","input_schema":{"type":"object","properties":{},"additionalProperties":false}}]}`)
	translated, err := geminiclaude.ConvertClaudeRequestToGemini("gemini-test", original, true)
	if err != nil {
		t.Fatal(err)
	}
	args := `{"reason":"literal reason","thoughtSignature":"literal signature","nested":{"readOnly":"literal"}}`
	raw := []byte(fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Open","args":%s}},{"functionCall":{"name":"Closed","args":{"reason":"synthetic"}}}]},"finishReason":"STOP"}]}`, args))
	nonstream := geminiclaude.ConvertGeminiResponseToClaudeNonStream(t.Context(), "gemini-test", original, translated, raw, nil)
	if gjson.GetBytes(nonstream, "content.0.input").Raw != args || gjson.GetBytes(nonstream, "content.1.input").Raw != "{}" {
		t.Fatalf("original schema openness was lost: %s", nonstream)
	}
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Open","args":{}}}]}}]}`,
		fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"functionCall":{"args":%s}}]}}]}`, args),
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Closed","args":{"reason":"synthetic"}}}]},"finishReason":"STOP"}]}`,
		`[DONE]`,
	}
	var state any
	inputs := map[int]string{}
	names := map[int]string{}
	for _, chunk := range chunks {
		for _, out := range geminiclaude.ConvertGeminiResponseToClaude(t.Context(), "gemini-test", original, translated, []byte(chunk), &state) {
			for _, line := range bytes.Split(out, []byte("\n")) {
				event := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
				index := int(event.Get("index").Int())
				if event.Get("content_block.type").String() == "tool_use" {
					names[index] = event.Get("content_block.name").String()
				}
				if event.Get("delta.type").String() == "input_json_delta" {
					inputs[index] += event.Get("delta.partial_json").String()
				}
			}
		}
	}
	if len(names) != 2 {
		t.Fatalf("tool count = %d, want 2", len(names))
	}
	for index, name := range names {
		want := args
		if name == "Closed" {
			want = "{}"
		}
		if !json.Valid([]byte(inputs[index])) || inputs[index] != want {
			t.Fatalf("%s emitted invalid or changed argument fragments: %q", name, inputs[index])
		}
	}
}

func TestUpgrade842ParallelPolicyAndCommentaryAcrossProtocols(t *testing.T) {
	for name, convert := range map[string]func(string, []byte, bool) ([]byte, error){"chat": codexchat.ConvertOpenAIRequestToCodex, "responses": codexresponses.ConvertOpenAIResponsesRequestToCodex} {
		t.Run(name, func(t *testing.T) {
			request := []byte(`{"model":"gpt-test","parallel_tool_calls":false,"messages":[{"role":"user","content":"answer"}],"input":"answer"}`)
			translated, err := convert("gpt-test", request, true)
			if err != nil || !gjson.GetBytes(translated, "parallel_tool_calls").Exists() || gjson.GetBytes(translated, "parallel_tool_calls").Bool() {
				t.Fatalf("explicit serial tool policy was lost: %s, %v", translated, err)
			}
		})
	}
	chunks := []string{
		`data: {"type":"response.created","response":{"id":"upgrade842","model":"gpt-test"}}`,
		`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking first."}]}}`,
		`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Final answer."}]}}`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":5,"input_tokens_details":{"cached_tokens":3}}}}`,
	}
	var claudeState, responsesState any
	var completed gjson.Result
	for _, chunk := range chunks {
		for _, claudeOut := range codexclaude.ConvertCodexResponseToClaude(t.Context(), "gpt-test", nil, nil, []byte(chunk), &claudeState) {
			for _, claudeLine := range bytes.Split(claudeOut, []byte("\n")) {
				if !bytes.HasPrefix(claudeLine, []byte("data: ")) {
					continue
				}
				for _, responseOut := range clauderesponses.ConvertClaudeResponseToOpenAIResponses(t.Context(), "claude", nil, nil, claudeLine, &responsesState) {
					for _, line := range bytes.Split(responseOut, []byte("\n")) {
						event := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
						if event.Get("type").String() == "response.completed" {
							completed = event.Get("response")
						}
					}
				}
			}
		}
	}
	messages := completed.Get(`output.#(type=="message")#`).Array()
	reasoning := completed.Get(`output.#(type=="reasoning")#`).Array()
	if len(messages) != 1 || messages[0].Get("content.0.text").String() != "Final answer." || len(reasoning) != 1 || reasoning[0].Get("summary.0.text").String() != "Checking first." || reasoning[0].Get("encrypted_content").String() != "" {
		t.Fatalf("commentary was duplicated as answer or assigned a signature: %s", completed.Raw)
	}
	if completed.Get("usage.input_tokens").Int() != 12 || completed.Get("usage.output_tokens").Int() != 5 || completed.Get("usage.input_tokens_details.cached_tokens").Int() != 3 {
		t.Fatalf("usage changed across protocol round trip: %s", completed.Raw)
	}
}
