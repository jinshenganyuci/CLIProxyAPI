package helps

import (
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"strings"
)

// GeminiPromptBlockErrorMessage identifies blocked prompts without response candidates.
func GeminiPromptBlockErrorMessage(payload []byte) (string, bool) {
	if len(payload) == 0 {
		return "", false
	}
	for _, cPath := range []string{"candidates", "response.candidates"} {
		if candidates := ojson.GetBytes(payload, cPath); candidates.IsArray() && len(candidates.Array()) > 0 {
			return "", false
		}
	}
	for _, prefix := range []string{"promptFeedback", "response.promptFeedback"} {
		reason := strings.TrimSpace(ojson.GetBytes(payload, prefix+".blockReason").String())
		if reason == "" {
			continue
		}
		if detail := strings.TrimSpace(ojson.GetBytes(payload, prefix+".blockReasonMessage").String()); detail != "" {
			return fmt.Sprintf("upstream blocked the prompt: %s - %s", reason, detail), true
		}
		return fmt.Sprintf("upstream blocked the prompt: %s", reason), true
	}
	return "", false
}
