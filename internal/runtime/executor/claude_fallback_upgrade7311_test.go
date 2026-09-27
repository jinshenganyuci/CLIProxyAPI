package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeFallbackUpgrade7311ActualRequestBetaGating(t *testing.T) {
	for _, tc := range []struct {
		name, key, extra, beta string
		want                   bool
	}{
		{"oauth_without_fallback", "sk-ant-oat-synthetic", "", "", false},
		{"oauth_token", "sk-ant-oat-synthetic", `,"fallback_credit_token":"synthetic-fct"`, "", true},
		{"oauth_explicit_fallback", "sk-ant-oat-synthetic", `,"fallbacks":[{"model":"claude-opus-5"}]`, "", true},
		{"key_without_fallback", "synthetic-key", "", "", false},
		{"key_token", "synthetic-key", `,"fallback_credit_token":"synthetic-fct"`, "", true},
		{"key_fallback_only", "synthetic-key", `,"fallbacks":[{"model":"claude-opus-5"}]`, "", false},
		{"explicit_beta", "synthetic-key", "", "fallback-credit-2026-06-01", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type observed struct {
				header http.Header
				body   []byte
			}
			seen := make(chan observed, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seen <- observed{r.Header.Clone(), body}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"synthetic","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: "fallback7311-" + uuid.NewString(), Provider: "claude", ProxyURL: "direct", Attributes: map[string]string{"api_key": tc.key, "base_url": server.URL, "cloak_mode": "always"}, Metadata: claudeOAuthTestMetadata()}
			payload := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]` + tc.extra + `}`)
			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{tc.beta}}})
			if err != nil {
				t.Fatal(err)
			}
			got := <-seen
			enabled := false
			for _, beta := range strings.Split(got.header.Get("Anthropic-Beta"), ",") {
				enabled = enabled || strings.TrimSpace(beta) == claudeFallbackCreditBeta
			}
			if enabled != tc.want {
				t.Fatalf("fallback credit beta=%t want=%t header=%s", enabled, tc.want, got.header.Get("Anthropic-Beta"))
			}
			if strings.Contains(tc.extra, "fallback_credit_token") && gjson.GetBytes(got.body, "fallback_credit_token").String() != "synthetic-fct" {
				t.Fatal("explicit fallback token changed")
			}
			if strings.Contains(tc.extra, "fallbacks") && gjson.GetBytes(got.body, "fallbacks.0.model").String() != "claude-opus-5" {
				t.Fatal("explicit fallback model changed")
			}
		})
	}
}
