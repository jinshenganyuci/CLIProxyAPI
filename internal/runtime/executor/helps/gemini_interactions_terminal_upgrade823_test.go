package helps

import "testing"

func TestGeminiInteractionsTerminalRequiresCompletionUpgrade823(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		terminal    bool
	}{
		{"complete-event", "data: {\"event_type\":\"interaction.complete\",\"interaction\":{\"usage\":{\"total_tokens\":33}}}\n\n", true},
		{"completed-event", "data: {\"event_type\":\"interaction.completed\"}\n\n", true},
		{"completed-object", "event: interaction.completed\ndata: {\"object\":\"interaction\",\"status\":\"completed\"}\n\n", true},
		{"transport-done", "data: [DONE]\n\n", true},
		{"usage-only", "data: {\"usage\":{\"total_tokens\":33}}\n\n", false},
		{"step-completed", "data: {\"event_type\":\"step.delta\",\"status\":\"completed\"}\n\n", false},
		{"failed", "data: {\"event_type\":\"interaction.failed\",\"object\":\"interaction\",\"status\":\"failed\"}\n\n", false},
		{"malformed", "event: interaction.completed\ndata: {broken}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsGeminiInteractionsTerminalFrame([]byte(tc.frame)); got != tc.terminal {
				t.Fatalf("terminal=%t, want %t", got, tc.terminal)
			}
		})
	}
}
