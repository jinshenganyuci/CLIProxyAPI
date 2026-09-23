package executor

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClaudeBetaUpgrade7315CacheEvictionUsesProtocolFields(t *testing.T) {
	for _, tt := range []struct {
		name, fields string
		want         bool
	}{
		{"root cache", `"cache_control":{"type":"ephemeral","evict_on_complete":true}`, true},
		{"system cache", `"system":[{"type":"text","text":"rule","cache_control":{"evict_on_complete":true}}]`, true},
		{"tool cache", `"tools":[{"name":"run","input_schema":{},"cache_control":{"evict_on_complete":true}}]`, true},
		{"message block cache", `"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"evict_on_complete":true}}]}]`, true},
		{"tool result block cache", `"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"text","text":"ok","cache_control":{"evict_on_complete":true}}]}]}]`, true},
		{"escaped protocol key", `"cache_control":{"type":"ephemeral","evict\u005fon_complete":true}`, true},
		{"literal user text", `"messages":[{"role":"user","content":"evict_on_complete"}]`, false},
		{"literal system text", `"system":"evict_on_complete"`, false},
		{"tool schema property", `"tools":[{"name":"run","input_schema":{"type":"object","properties":{"evict_on_complete":{"type":"boolean"}}}}]`, false},
		{"tool input data", `"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"run","input":{"cache_control":{"evict_on_complete":true}}}]}]`, false},
		{"metadata data", `"metadata":{"note":"evict_on_complete"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"claude-opus-5-5",` + tt.fields + `}`)
			original := bytes.Clone(body)
			for _, stream := range []bool{false, true} {
				auth := fixtureAuth()
				req, errNew := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
				if errNew != nil {
					t.Fatal(errNew)
				}
				if errHeaders := applyClaudeHeaders(req, auth, auth.Attributes[cliproxyauth.AttributeAPIKey], stream, nil, body, &config.Config{}, nil, false); errHeaders != nil {
					t.Fatal(errHeaders)
				}
				got := strings.Contains(req.Header.Get("Anthropic-Beta"), claudePromptCachingEvictBeta)
				if got != tt.want {
					t.Errorf("stream=%v cache eviction beta=%v, want %v", stream, got, tt.want)
				}
				if !bytes.Equal(body, original) {
					t.Fatal("header assembly mutated the body")
				}
			}
		})
	}
	if got := claudeCodeCLIBetas([]byte(`{"model":"claude-opus-5-5"}`), map[string]bool{claudePromptCachingEvictBeta: true}, false); !strings.Contains(got, claudePromptCachingEvictBeta) {
		t.Fatal("explicit cache eviction beta was lost")
	}
}
