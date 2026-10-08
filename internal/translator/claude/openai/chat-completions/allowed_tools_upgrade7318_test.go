package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestClaudeAllowedToolsUpgrade7318PreservesRestrictionsAndNativeNames(t *testing.T) {
	const rawName = "mcp.server:get_time"
	const nativeName = "mcp_server_get_time"
	for _, tc := range []struct {
		name        string
		choice      string
		declaration string
		wantChoice  string
		wantTool    bool
	}{
		{"flat raw name", `{"type":"allowed_tools","tools":[{"type":"function","name":"mcp.server:get_time"}],"mode":"required"}`, rawName, "any", true},
		{"nested raw name", `{"type":"allowed_tools","allowed_tools":{"tools":[{"type":"function","function":{"name":"mcp.server:get_time"}}],"mode":"auto"}}`, rawName, "auto", true},
		{"sanitized allow name", `{"type":"allowed_tools","tools":[{"type":"function","name":"mcp_server_get_time"}]}`, rawName, "auto", true},
		{"already sanitized declaration", `{"type":"allowed_tools","tools":[{"type":"function","name":"mcp.server:get_time"}]}`, nativeName, "auto", true},
		{"explicit empty nested list wins", `{"type":"allowed_tools","allowed_tools":{"tools":[]},"tools":[{"type":"function","name":"mcp.server:get_time"}]}`, rawName, "none", false},
		{"invalid nested container stays closed", `{"type":"allowed_tools","allowed_tools":false,"tools":[{"type":"function","name":"mcp.server:get_time"}]}`, rawName, "none", false},
		{"invalid nested list stays closed", `{"type":"allowed_tools","allowed_tools":{"tools":"mcp.server:get_time"},"tools":[{"type":"function","name":"mcp.server:get_time"}]}`, rawName, "none", false},
		{"invalid mode stays closed", `{"type":"allowed_tools","mode":"disabled","tools":[{"type":"function","name":"mcp.server:get_time"}]}`, rawName, "none", false},
		{"nonfunction allow item stays closed", `{"type":"allowed_tools","tools":[{"type":"web_search","name":"mcp.server:get_time"}]}`, rawName, "none", false},
		{"empty flat list stays closed", `{"type":"allowed_tools","tools":[]}`, rawName, "none", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call:time","type":"function","function":{"name":"mcp.server:get_time","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call:time","content":"stale result"},{"role":"tool","tool_call_id":"call:time","content":"latest result"}],"tools":[{"type":"function","function":{}},{"type":"function","function":{"name":"unrelated"}}]}`)
			body, _ = sjson.SetBytes(body, "tools.0.function.name", tc.declaration)
			body, _ = sjson.SetRawBytes(body, "tool_choice", []byte(tc.choice))
			out, errConvert := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", body, false)
			if errConvert != nil {
				t.Fatalf("request translation failed: %v", errConvert)
			}
			if got := gjson.GetBytes(out, "tool_choice.type").String(); got != tc.wantChoice {
				t.Fatalf("tool_choice=%q, want %q; payload=%s", got, tc.wantChoice, out)
			}
			tools := gjson.GetBytes(out, "tools").Array()
			if tc.wantTool {
				if len(tools) != 1 || tools[0].Get("name").String() != nativeName || tools[0].Get("input_schema.type").String() != "object" {
					t.Fatalf("selected tool name or parameterless schema changed: %s", out)
				}
			} else if len(tools) != 0 {
				t.Fatalf("empty or invalid restriction broadened the allowed tools: %s", out)
			}
			messages := gjson.GetBytes(out, "messages").Array()
			if len(messages) != 2 {
				t.Fatalf("duplicate tool results were not coalesced: %s", out)
			}
			call := messages[0].Get("content.0")
			result := messages[1].Get("content.0")
			if call.Get("name").String() != nativeName || call.Get("id").String() != "call_time" || result.Get("tool_use_id").String() != "call_time" || result.Get("content").String() != "latest result" {
				t.Fatalf("native name normalization broke the fork's latest-result pairing: %s", out)
			}
		})
	}
}
