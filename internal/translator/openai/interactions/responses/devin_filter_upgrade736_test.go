package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade736InteractionsDevinFilteringIsProviderScoped(t *testing.T) {
	const payload = `{"input":"hello","tools":[{"type":"namespace","name":"mcp__codex_app","tools":[{"type":"function","name":"automation_update","description":"Update an automation","parameters":{"type":"object"}},{"type":"function","name":"read_resource","description":"Read a resource","parameters":{"type":"object"}}]},{"type":"function","name":"exec_command","description":"Runs a command, returning output or a session ID for ongoing interaction.","parameters":{"type":"object"}},{"type":"function","name":"write_stdin","description":"Writes characters to an existing unified exec session and returns recent output.","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"automation_update","namespace":"mcp__codex_app"}}`
	for _, tc := range []struct {
		model string
		devin bool
	}{
		{model: "gemini-3.1-pro"},
		{model: "gemini-3.1-flash-lite"},
		{model: "my-native-interactions-alias"},
		{model: "antigravity-preview-05-2026"},
		{model: "devin/swe-2", devin: true},
		{model: "devin/gemini-3-7-flash", devin: true},
	} {
		for _, stream := range []bool{false, true} {
			name := tc.model + "/nonstream"
			if stream {
				name = tc.model + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				out := ConvertOpenAIResponsesRequestToInteractions(tc.model, []byte(payload), stream)
				descriptions := make(map[string]string)
				for _, tool := range gjson.GetBytes(out, "tools").Array() {
					descriptions[tool.Get("name").String()] = tool.Get("description").String()
				}
				_, hasAutomation := descriptions["mcp__codex_app__automation_update"]
				if hasAutomation == tc.devin {
					t.Errorf("automation tool present = %v, want %v", hasAutomation, !tc.devin)
				}
				choice := gjson.GetBytes(out, "generation_config.tool_choice")
				if choice.Exists() == tc.devin {
					t.Errorf("tool choice present = %v, want %v", choice.Exists(), !tc.devin)
				}
				if !tc.devin && choice.Get("name").String() != "mcp__codex_app__automation_update" {
					t.Errorf("tool choice lost namespace-qualified name: %s", choice.Raw)
				}
				wantExec := "Runs a command, returning output or a session ID for ongoing interaction."
				wantStdin := "Writes characters to an existing unified exec session and returns recent output."
				if tc.devin {
					wantExec = "Runs a command, returning output or an session ID for ongoing interaction."
					wantStdin = "Writes characters to a existing unified exec session and returns recent output."
				}
				if got := descriptions["exec_command"]; got != wantExec {
					t.Errorf("exec description = %q, want %q", got, wantExec)
				}
				if got := descriptions["write_stdin"]; got != wantStdin {
					t.Errorf("stdin description = %q, want %q", got, wantStdin)
				}
				if got := descriptions["mcp__codex_app__read_resource"]; got != "Read a resource" {
					t.Errorf("unrelated tool changed: %q", got)
				}
			})
		}
	}
}
