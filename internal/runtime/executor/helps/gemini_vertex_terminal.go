package helps

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

// IsGeminiVertexTerminalStreamChunk recognizes a terminal after downstream delivery.
func IsGeminiVertexTerminalStreamChunk(chunk []byte) bool {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if bytes.Equal(trimmed, []byte("data: [DONE]")) || bytes.Equal(trimmed, []byte("[DONE]")) {
			return true
		}
		payload := JSONPayload(line)
		if len(payload) == 0 {
			continue
		}
		eventType := gjson.GetBytes(payload, "type").String()
		if eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.done" || eventType == "message_stop" {
			return true
		}
		for _, path := range []string{
			"choices.0.finish_reason",
			"response.choices.0.finish_reason",
			"candidates.0.finishReason",
			"response.candidates.0.finishReason",
		} {
			if finishReason := gjson.GetBytes(payload, path); finishReason.Exists() && strings.TrimSpace(finishReason.String()) != "" {
				return true
			}
		}
	}
	return false
}
