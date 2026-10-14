package executor

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

func TestClaudeHybridMCPExplicitPassthroughWinsOverClientSuffix(t *testing.T) {
	const virtual = "mcp__ripple_middle__"
	reverseMap := map[string]string{
		virtual + "blanket_Bash": "Bash",
		"mcp__shell__Bash":       "mcp__shell__Bash",
		"mcp__other__Bash":       "mcp__other__Bash",
	}
	for _, scenario := range []struct{ input, want string }{
		{virtual + "shell__Bash", "mcp__shell__Bash"},
		{virtual + "mcp__shell__Bash", "mcp__shell__Bash"},
		{virtual + "mcp__other__Bash", "mcp__other__Bash"},
		{virtual + "ripple_middle__mcp__shell__Bash", "mcp__shell__Bash"},
		{virtual + "other__Bash", "mcp__other__Bash"},
		{virtual + "ripple_middle__shell__Bash", "mcp__shell__Bash"},
		{virtual + "Bash", "Bash"},
		{virtual + "blanket_Bash", "Bash"},
		{"mcp__shell__Bash", "mcp__shell__Bash"},
		{"mcp__unknown__shell__Bash", "mcp__unknown__shell__Bash"},
	} {
		t.Run(scenario.input, func(t *testing.T) {
			response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"tool-upgrade","name":%q,"input":{"keep":"value"}}]}`, scenario.input))
			restored, errRestore := restoreClaudeOAuthToolNamesFromResponse(response, reverseMap)
			if errRestore != nil || gjson.GetBytes(restored, "content.0.name").String() != scenario.want || gjson.GetBytes(restored, "content.0.input.keep").String() != "value" {
				t.Fatalf("response restoration = %s/%v, want %s with unchanged arguments", restored, errRestore, scenario.want)
			}
			line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-upgrade","name":%q,"input":{"keep":"value"}}}`, scenario.input))
			restoredLine, errStream := restoreClaudeOAuthToolNamesFromStreamLine(line, reverseMap)
			payload := helps.JSONPayload(restoredLine)
			if errStream != nil || gjson.GetBytes(payload, "content_block.name").String() != scenario.want || gjson.GetBytes(payload, "content_block.input.keep").String() != "value" {
				t.Fatalf("stream restoration = %s/%v, want %s with unchanged arguments", restoredLine, errStream, scenario.want)
			}
		})
	}
}
