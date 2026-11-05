package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

func TestV855ClaudeDirectiveUsageIgnoresCachedNonePlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"adaptive none placeholder", `"thinking":{"type":"adaptive"},"output_config":{"effort":"none"}`, "high"},
		{"zero budget placeholder", `"thinking":{"type":"enabled","budget_tokens":0}`, "high"},
		{"explicit disabled", `"thinking":{"type":"disabled"},"output_config":{"effort":"none"}`, "none"},
		{"explicit between tools", `"thinking":{"type":"between_tools"},"output_config":{"effort":"none"}`, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{` + tc.config + `,"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"continue"}]}`)
			if got := thinking.ExtractReasoningEffort(body, "claude", "claude-opus-5-5"); got != tc.want {
				t.Errorf("source effort = %q, want %q", got, tc.want)
			}
			if got := thinking.ExtractTranslatedReasoningEffort(body, "claude"); got != tc.want {
				t.Errorf("effective effort = %q, want %q", got, tc.want)
			}
		})
	}
}
