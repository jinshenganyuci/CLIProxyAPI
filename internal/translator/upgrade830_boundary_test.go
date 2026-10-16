package translator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	antigravityclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/claude"
	clauderesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

func TestUpgrade830SchemaCleanupKeepsConversationData(t *testing.T) {
	request := []byte(`{"model":"gemini-test","tools":[{"name":"render","input_schema":{"type":"object","properties":{"discriminator":{"type":"string"},"value":{"anyOf":[{"type":"string"},{"type":"integer"},{"type":"null"}],"discriminator":{"propertyName":"kind"}}}}}],"messages":[{"role":"user","content":"render"},{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"render","input":{"discriminator":"literal","value":12,"title":"literal-title"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}]}`)
	for _, route := range []string{"gemini", "antigravity"} {
		t.Run(route, func(t *testing.T) {
			converted := translator.Request("claude", route, "gemini-test", request, false)
			root := gjson.ParseBytes(converted)
			if route == "antigravity" {
				root = root.Get("request")
			}
			declaration := root.Get("tools.0.functionDeclarations.0")
			schema := declaration.Get("parametersJsonSchema")
			if !schema.Exists() {
				schema = declaration.Get("parameters")
			}
			if !schema.Exists() || schema.Get("properties.value.discriminator").Exists() || schema.Get("properties.discriminator.type").String() != "string" || len(schema.Get("properties.value.anyOf").Array()) < 2 {
				t.Fatalf("schema metadata or preserved union contract changed: %s", converted)
			}
			var args gjson.Result
			for _, content := range root.Get("contents").Array() {
				for _, part := range content.Get("parts").Array() {
					if part.Get("functionCall.name").String() == "render" {
						args = part.Get("functionCall.args")
					}
				}
			}
			if args.Get("discriminator").String() != "literal" || args.Get("title").String() != "literal-title" || args.Get("value").Int() != 12 {
				t.Fatalf("schema cleanup changed conversation data: %s", converted)
			}
			if route == "antigravity" {
				flattened := gjson.Parse(util.CleanJSONSchemaForAntigravityClaudeTool(schema.Raw, false))
				if flattened.Get("properties.value.anyOf").Exists() || !flattened.Get("properties.value.nullable").Bool() || flattened.Get("properties.discriminator.type").String() != "string" {
					t.Fatalf("Claude second pass lost nullability or retained a union: %s", flattened.Raw)
				}
			}
		})
	}
}

func TestUpgrade830ConcurrentGeminiToolIDsReplayToClaude(t *testing.T) {
	request := []byte(`{"model":"gemini-test","tools":[{"name":"render","input_schema":{"type":"object"}}]}`)
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"render","args":{"value":12}}}]},"finishReason":"STOP"}]}`)
	const workers = 32
	ids := make(chan string, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			if i%2 == 0 {
				converted := geminiclaude.ConvertGeminiResponseToClaudeNonStream(t.Context(), "gemini-test", request, request, raw, nil)
				ids <- gjson.GetBytes(converted, "content.0.id").String()
				return
			}
			var state any
			out := bytes.Join(geminiclaude.ConvertGeminiResponseToClaude(t.Context(), "gemini-test", request, request, raw, &state), nil)
			for _, line := range bytes.Split(out, []byte("\n")) {
				payload := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
				if payload.Get("content_block.type").String() == "tool_use" {
					ids <- payload.Get("content_block.id").String()
					return
				}
			}
			ids <- ""
		})
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	var input []map[string]any
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("stream and nonstream calls produced an empty or reused ID: %q", id)
		}
		seen[id] = true
		input = append(input, map[string]any{"type": "function_call", "call_id": id, "name": "render", "arguments": `{"value":12}`}, map[string]any{"type": "function_call_output", "call_id": id, "output": "ok"})
	}
	replay, errMarshal := json.Marshal(map[string]any{"input": input})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	converted, errConvert := clauderesponses.ConvertOpenAIResponsesRequestToClaude("claude-test", replay, false)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	uses, results := 0, 0
	for _, message := range gjson.GetBytes(converted, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "tool_use":
				uses++
			case "tool_result":
				results++
			}
		}
	}
	if uses != workers || results != workers {
		t.Fatalf("replay deduplicated distinct turns: uses=%d results=%d", uses, results)
	}
}

func TestUpgrade830CachedUsageAcrossAntigravityClaudeResponses(t *testing.T) {
	request := []byte(`{"model":"claude-test"}`)
	raw := []byte(`{"response":{"responseId":"cached-test","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90,"candidatesTokenCount":0,"thoughtsTokenCount":2,"totalTokenCount":107}}}`)
	var agState, responseState any
	var terminal gjson.Result
	for _, chunk := range [][]byte{raw, []byte("[DONE]")} {
		for _, output := range antigravityclaude.ConvertAntigravityResponseToClaude(t.Context(), "claude-test", request, request, chunk, &agState) {
			for _, line := range bytes.Split(output, []byte("\n")) {
				if !bytes.HasPrefix(line, []byte("data: ")) {
					continue
				}
				for _, event := range clauderesponses.ConvertClaudeResponseToOpenAIResponses(t.Context(), "claude-test", request, request, line, &responseState) {
					for _, eventLine := range bytes.Split(event, []byte("\n")) {
						payload := gjson.ParseBytes(bytes.TrimPrefix(eventLine, []byte("data: ")))
						if payload.Get("type").String() == "response.completed" {
							terminal = payload.Get("response")
						}
					}
				}
			}
		}
	}
	for path, want := range map[string]int64{"usage.input_tokens": 100, "usage.input_tokens_details.cached_tokens": 90, "usage.output_tokens": 7, "usage.total_tokens": 107} {
		if terminal.Get(path).Int() != want {
			t.Fatalf("%s = %d, want %d: %s", path, terminal.Get(path).Int(), want, terminal.Raw)
		}
	}
}

func TestUpgrade830IncompleteToolOutputDedupUsesLatestStatus(t *testing.T) {
	for _, tc := range []struct {
		first, last string
		wantError   bool
	}{
		{"completed", " InCoMpLeTe ", true},
		{"incomplete", "completed", false},
	} {
		t.Run(tc.last, func(t *testing.T) {
			request := []byte(fmt.Sprintf(`{"input":[{"type":"function_call","call_id":"call-1","name":"render","arguments":"{}"},{"type":"function_call_output","call_id":"call-1","status":%q,"output":"old"},{"type":"function_call_output","call_id":"call-1","status":%q,"output":"latest"}]}`, tc.first, tc.last))
			converted, errConvert := clauderesponses.ConvertOpenAIResponsesRequestToClaude("claude-test", request, false)
			if errConvert != nil {
				t.Fatal(errConvert)
			}
			count := 0
			for _, message := range gjson.GetBytes(converted, "messages").Array() {
				for _, block := range message.Get("content").Array() {
					if block.Get("type").String() == "tool_result" {
						count++
						if block.Get("content").String() != "latest" || block.Get("is_error").Bool() != tc.wantError {
							t.Fatalf("latest output and status diverged: %s", block.Raw)
						}
					}
				}
			}
			if count != 1 {
				t.Fatalf("result count = %d, want 1", count)
			}
		})
	}
}
