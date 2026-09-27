package common_test

import (
	"bytes"
	"fmt"
	"testing"

	agclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/claude"
	aginteractions "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/interactions"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	geminiinteractions "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/interactions"
	geminiresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	"github.com/tidwall/gjson"
)

func TestGeminiRefUpgrade7316RoutesPreserveBusinessData(t *testing.T) {
	claude := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":%s}]}]}`
	responses := `{"input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":%s}]}`
	interactions := `{"input":[{"type":"function_result","call_id":"call_1","name":"lookup","result":%s}]}`
	for _, route := range []struct {
		name, template, path string
		convert              func(string, []byte, bool) []byte
		directResponse       bool
	}{
		{"claude-gemini", claude, "contents.1.parts.0.functionResponse.response.result", geminiclaude.ConvertClaudeRequestToGemini, false},
		{"claude-antigravity", claude, "request.contents.1.parts.0.functionResponse.response.result", agclaude.ConvertClaudeRequestToAntigravity, false},
		{"responses-gemini", responses, "contents.1.parts.0.functionResponse.response.result", geminiresponses.ConvertOpenAIResponsesRequestToGemini, false},
		{"interactions-gemini", interactions, "contents.0.parts.0.functionResponse.response", geminiinteractions.ConvertInteractionsRequestToGemini, true},
		{"interactions-antigravity", interactions, "request.contents.0.parts.0.functionResponse.response", aginteractions.ConvertInteractionsRequestToAntigravity, true},
	} {
		for _, payload := range []struct {
			name, raw string
			stringify bool
		}{
			{"schema-ref", `{ "schema":{"$ref":"#/defs/Thing"}, "n":9223372036854775807,"ok":false,"empty":null,"text":"<>&\\u0000" }`, true},
			{"business-object", `{ "n":9223372036854775807,"ok":false,"empty":null,"description":"$ref is only text" }`, false},
			{"nonstring-ref", `{"schema":{"$ref":false},"data":[{"$ref":null},{"$ref":9223372036854775807}]}`, false},
		} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", route.name, payload.name, stream), func(t *testing.T) {
					raw := []byte(fmt.Sprintf(route.template, payload.raw))
					before := bytes.Clone(raw)
					out := route.convert("gemini-3.8-flash", raw, stream)
					path := route.path
					if route.directResponse && payload.stringify {
						path += ".result"
					}
					got := gjson.GetBytes(out, path)
					if payload.stringify {
						if got.Type != gjson.String || got.String() != payload.raw {
							t.Fatalf("opaque JSON text changed at %s: %s", path, out)
						}
					} else if !got.IsObject() || got.Raw != payload.raw {
						t.Fatalf("business JSON changed at %s: %s", path, out)
					}
					if !bytes.Equal(raw, before) {
						t.Fatal("caller input buffer changed")
					}
				})
			}
		}
	}
}

func TestGeminiRefUpgrade7316HelperPreservesFalseNullAndLargeNumbers(t *testing.T) {
	for _, raw := range []string{`false`, `null`, `0`, `9223372036854775807`, `[false,null,9223372036854775807]`, `"$ref"`} {
		for _, path := range []string{"response.result", "functionResponse.response.result", "functionResponse.response"} {
			t.Run(path+"/"+raw, func(t *testing.T) {
				out := common.SetGeminiFunctionResponseRaw([]byte(`{}`), path, raw)
				if got := gjson.GetBytes(out, path).Raw; got != raw {
					t.Fatalf("value changed: got %s, want %s", got, raw)
				}
			})
		}
	}
}
