package translator

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	agclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/claude"
	claudechat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/chat-completions"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/claude"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	interactionsclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/interactions/claude"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/claude"
	chatresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	"github.com/tidwall/gjson"
)

func TestUpgrade844ResponsesCommentaryRegisteredReplay(t *testing.T) {
	request := []byte(`{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"commentary-only"}],"encrypted_content":" cpa:commentary:v1 "},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"literal cpa:commentary:v1"}]},{"type":"function_call","call_id":"call844","name":"Inspect","arguments":"{\"signature\":\"cpa:commentary:v1\"}"},{"type":"function_call_output","call_id":"call844","output":"result cpa:commentary:v1"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}],"tools":[{"type":"function","name":"Inspect","parameters":{"type":"object","properties":{"signature":{"type":"string"}}}}]}`)
	before := bytes.Clone(request)
	for target, model := range map[string]string{"codex": "gpt-5.6", "claude": "claude-opus-4-6", "gemini": "gemini-3.1-pro", "antigravity": "claude-opus-4-6", "openai": "custom", "interactions": "gemini-3.1-pro"} {
		t.Run(target, func(t *testing.T) {
			out := translator.Request("openai-response", target, model, request, false)
			for _, text := range []string{"commentary-only", "literal cpa:commentary:v1", "result cpa:commentary:v1"} {
				if bytes.Count(out, []byte(text)) != 1 {
					t.Errorf("%q must survive exactly once: %s", text, out)
				}
			}
			if target == "codex" && (gjson.GetBytes(out, `input.#(phase=="commentary").content.0.text`).String() != "commentary-only" || gjson.GetBytes(out, `input.#(type=="reasoning").encrypted_content`).Exists()) {
				t.Errorf("Codex commentary phase/control boundary lost: %s", out)
			}
			if bytes.Count(out, []byte(signature.CodexCommentarySignatureMarker)) != 3 {
				t.Errorf("expected only visible/tool literals, without control carrier: %s", out)
			}
			if !bytes.Equal(request, before) {
				t.Fatal("request buffer was mutated")
			}
		})
	}
}

func TestUpgrade844CommentaryMarkerControlAndLiteralIsolation(t *testing.T) {
	request := []byte(`{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"thinking","thinking":"commentary-only","signature":" cpa:commentary:v1 "},{"type":"text","text":"literal cpa:commentary:v1"},{"type":"tool_use","id":"call","name":"Inspect","input":{"signature":"cpa:commentary:v1","thinking":"literal-argument"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"result cpa:commentary:v1"}]}],"tools":[{"name":"Inspect","input_schema":{"type":"object","properties":{"signature":{"type":"string"},"thinking":{"type":"string"}}}}]}`)
	before := bytes.Clone(request)
	for name, convert := range map[string]func(string, []byte, bool) ([]byte, error){
		"codex":         codexclaude.ConvertClaudeRequestToCodex,
		"codex_compat":  codexclaude.ConvertClaudeRequestToCodexWithCompat,
		"openai":        openaiclaude.ConvertClaudeRequestToOpenAI,
		"openai_compat": openaiclaude.ConvertClaudeRequestToOpenAIWithCompat,
		"gemini":        geminiclaude.ConvertClaudeRequestToGemini,
		"gemini_compat": geminiclaude.ConvertClaudeRequestToGeminiWithCompat,
		"antigravity":   agclaude.ConvertClaudeRequestToAntigravity,
		"interactions":  interactionsclaude.ConvertClaudeRequestToInteractions,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := convert("custom-model", request, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"commentary-only", "literal cpa:commentary:v1", "literal-argument", "result cpa:commentary:v1"} {
				if !bytes.Contains(out, []byte(text)) {
					t.Fatalf("lost commentary or literal text %q", text)
				}
			}
			if !bytes.Equal(request, before) {
				t.Fatal("request buffer was mutated")
			}
			if name == "codex" || name == "codex_compat" {
				if gjson.GetBytes(out, `input.#(phase=="commentary").content.0.text`).String() != "commentary-only" {
					t.Fatal("commentary phase was not restored")
				}
				if gjson.GetBytes(out, `input.#(type=="reasoning").encrypted_content`).String() == signature.CodexCommentarySignatureMarker {
					t.Fatal("internal marker was treated as an upstream signature")
				}
			}
		})
	}
	// The direct Claude sanitizer must not inspect nested tool argument objects.
	restored, count := signature.RestoreCodexCommentaryMarkers(request)
	if count != 1 || gjson.GetBytes(restored, "messages.1.content.0.type").String() != "text" || gjson.GetBytes(restored, "messages.1.content.2.input.signature").String() != signature.CodexCommentarySignatureMarker {
		t.Fatal("marker restoration changed a nested literal or failed to consume the control marker")
	}
}

func TestUpgrade844ClaudeThinkingUsageAcrossChatResponses(t *testing.T) {
	for _, details := range []string{"", `,"output_tokens_details":{"thinking_tokens":0}`, `,"output_tokens_details":{"thinking_tokens":7}`} {
		t.Run(fmt.Sprintf("details=%s", details), func(t *testing.T) {
			usage := `{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":3,"output_tokens":12` + details + `}`
			raw := []byte(`{"id":"usage844","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":` + usage + `}`)
			chat := claudechat.ConvertClaudeResponseToOpenAINonStream(t.Context(), "claude", nil, nil, raw, nil)
			plain := chatresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(t.Context(), "claude", nil, nil, chat, nil)
			wantReasoning := gjson.Get(usage, "output_tokens_details.thinking_tokens").Int()
			if got := gjson.GetBytes(chat, "usage.completion_tokens_details.reasoning_tokens"); got.Exists() != (details != "") || got.Int() != wantReasoning {
				t.Fatal("explicit zero or absent thinking usage was not preserved in Chat")
			}
			var chatState, responsesState any
			var completed gjson.Result
			chunks := []string{`data: {"type":"message_start","message":{"id":"usage844","model":"claude","usage":{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":3,"output_tokens":0}}}`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`, `data: {"type":"content_block_stop","index":0}`, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":` + usage + `}`, `data: {"type":"message_stop"}`}
			accept := func(chatChunk []byte) {
				for _, out := range chatresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(t.Context(), "claude", nil, nil, chatChunk, &responsesState) {
					for _, line := range bytes.Split(out, []byte("\n")) {
						e := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
						if e.Get("type").String() == "response.completed" {
							completed = e.Get("response")
						}
					}
				}
			}
			for _, chunk := range chunks {
				for _, out := range claudechat.ConvertClaudeResponseToOpenAI(t.Context(), "claude", nil, nil, []byte(chunk), &chatState) {
					accept(out)
				}
			}
			accept([]byte("data: [DONE]"))
			for _, out := range []gjson.Result{gjson.ParseBytes(plain), completed} {
				for field, want := range map[string]int64{"input_tokens": 17, "output_tokens": 12, "total_tokens": 29, "input_tokens_details.cached_tokens": 4, "output_tokens_details.reasoning_tokens": wantReasoning} {
					if out.Get("usage."+field).Int() != want {
						t.Fatalf("%s = %d, want %d: %s", field, out.Get("usage."+field).Int(), want, out.Raw)
					}
				}
			}
		})
	}
}
