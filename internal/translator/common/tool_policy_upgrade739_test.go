package common_test

import (
	"fmt"
	"testing"

	claudechat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/chat-completions"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	geminichat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/chat-completions"
	"github.com/tidwall/gjson"
)

func TestToolPolicyUpgrade739UnknownRestrictionsStayClosed(t *testing.T) {
	openAITools := `[{"type":"function","function":{"name":"_1tool","parameters":{"type":"object"}}}]`
	claudeTools := `[{"name":"_1tool","input_schema":{"type":"object"}}]`
	for _, tc := range []struct {
		name, choice, extra string
		claudeInput         bool
	}{
		{"unknown_string", `"unexpected"`, "", false},
		{"unknown_object", `{"type":"unexpected"}`, "", false},
		{"boolean", `false`, "", false},
		{"missing_function_name", `{"type":"function"}`, "", false},
		{"unknown_allow_mode", `{"type":"allowed_tools","mode":"never","tools":[{"type":"function","name":"_1tool"}]}`, "", false},
		{"nested_empty_not_overridden", `{"type":"allowed_tools","allowed_tools":{"tools":[]},"tools":[{"type":"function","name":"_1tool"}]}`, "", false},
		{"wrong_allowed_tool_type", `{"type":"allowed_tools","tools":[{"type":"web_search","name":"_1tool"}]}`, "", false},
		{"claude_missing_name", `{"type":"tool"}`, "", true},
		{"claude_undeclared_sanitized_alias", `{"type":"tool","name":"1tool"}`, "", true},
		{"claude_unknown_choice", `{"type":"unexpected"}`, "", true},
		{"claude_disable_parallel", `{"type":"auto","disable_parallel_tool_use":true}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := openAITools
			if tc.claudeInput {
				tools = claudeTools
			}
			input := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"hello"}],"tools":%s,"tool_choice":%s%s}`, tools, tc.choice, tc.extra))
			if tc.claudeInput {
				out, errConvert := geminiclaude.ConvertClaudeRequestToGemini("gemini-3-flash", input, false)
				if errConvert != nil {
					t.Fatalf("request translation failed: %v", errConvert)
				}
				if got := gjson.GetBytes(out, "toolConfig.functionCallingConfig.mode").String(); got != "NONE" {
					t.Fatalf("explicit restriction broadened to %q: %s", got, out)
				}
				return
			}
			for _, target := range []struct {
				name, path string
				convert    func(string, []byte, bool) ([]byte, error)
				want       string
			}{
				{"claude", "tool_choice.type", claudechat.ConvertOpenAIRequestToClaude, "none"},
				{"gemini", "toolConfig.functionCallingConfig.mode", geminichat.ConvertOpenAIRequestToGemini, "NONE"},
			} {
				out, errConvert := target.convert("test-model", input, false)
				if errConvert != nil {
					t.Fatalf("request translation failed: %v", errConvert)
				}
				if got := gjson.GetBytes(out, target.path).String(); got != target.want {
					t.Errorf("%s explicit restriction broadened to %q: %s", target.name, got, out)
				}
			}
		})
	}
}

func TestToolPolicyUpgrade739GeminiBuiltinsRespectRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name, choice, extra, wantMode string
		wantBuiltins                  bool
	}{
		{"default", "", "", "", true},
		{"auto", `"auto"`, "", "AUTO", true},
		{"allow_subset", `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"tool_a"}]}`, "", "AUTO", false},
		{"specific_function", `{"type":"function","function":{"name":"tool_a"}}`, "", "ANY", false},
		{"none", `"none"`, "", "NONE", false},
		{"unknown", `"unexpected"`, "", "NONE", false},
		{"no_parallel", `"auto"`, `,"parallel_tool_calls":false`, "NONE", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			choice := ""
			if tc.choice != "" {
				choice = `,"tool_choice":` + tc.choice
			}
			input := []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"tool_a","parameters":{"type":"object"}}},{"google_search":{}},{"code_execution":{}},{"url_context":{}}]` + choice + tc.extra + `}`)
			out, errConvert := geminichat.ConvertOpenAIRequestToGemini("gemini-3-flash", input, false)
			if errConvert != nil {
				t.Fatalf("request translation failed: %v", errConvert)
			}
			if got := gjson.GetBytes(out, "toolConfig.functionCallingConfig.mode").String(); got != tc.wantMode {
				t.Fatalf("mode=%q want=%q", got, tc.wantMode)
			}
			count := 0
			for _, tool := range gjson.GetBytes(out, "tools").Array() {
				if tool.Get("googleSearch").Exists() || tool.Get("codeExecution").Exists() || tool.Get("urlContext").Exists() {
					count++
				}
			}
			if (count > 0) != tc.wantBuiltins {
				t.Fatalf("built-in count=%d want enabled=%t: %s", count, tc.wantBuiltins, out)
			}
		})
	}
}

func TestToolPolicyUpgrade739ClaudeGeminiNameCollisions(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"1tool","input_schema":{"type":"object"}},{"name":"_1tool","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"_1tool"}}`)
	out, errConvert := geminiclaude.ConvertClaudeRequestToGemini("gemini-3-flash", input, false)
	if errConvert != nil {
		t.Fatalf("request translation failed: %v", errConvert)
	}
	if mode := gjson.GetBytes(out, "toolConfig.functionCallingConfig.mode").String(); mode != "NONE" {
		t.Fatalf("ambiguous sanitized declarations can call wrong tool: %s", out)
	}
}
