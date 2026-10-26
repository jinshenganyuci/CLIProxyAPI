package executor

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// A Responses client can replay the reasoning carrier produced by a previous
// Codex-to-Claude-to-Responses conversion. It is not an upstream signature.
func TestV844CommentaryResponsesReplayDoesNotSendInternalSignature(t *testing.T) {
	for _, tc := range []struct {
		from   sdktranslator.Format
		stream bool
	}{
		{sdktranslator.FormatOpenAIResponse, false},
		{sdktranslator.FormatOpenAIResponse, true},
		{sdktranslator.FormatCodex, false},
		{sdktranslator.FormatCodex, true},
	} {
		t.Run(fmt.Sprintf("%s_stream_%t", tc.from, tc.stream), func(t *testing.T) {
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
				}
				captured <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp844\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
			}))
			defer server.Close()
			cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "synthetic844", BaseURL: server.URL, Models: []config.CodexModel{{Name: "DeepSeek-V4.1-Flash", Alias: "fixture844", IsCompat: true}}}}}
			exec := NewCodexExecutor(cfg)
			auth := &coreauth.Auth{Provider: "codex", ProxyURL: "direct", Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic844"}}
			payload := []byte(`{"model":"fixture844","input":[{"type":"reasoning","id":"rs844","summary":[{"type":"summary_text","text":"COMMENTARY_SENTINEL"}],"encrypted_content":"cpa:commentary:v1"},{"type":"message","role":"user","content":[{"type":"input_text","text":"literal cpa:commentary:v1"}]},{"type":"function_call_output","call_id":"call844","output":"tool cpa:commentary:v1"}]}`)
			req := coreexecutor.Request{Model: "fixture844", Payload: payload}
			opts := coreexecutor.Options{SourceFormat: tc.from, Stream: tc.stream}
			if tc.stream {
				result, errStream := exec.ExecuteStream(t.Context(), auth, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, errExecute := exec.Execute(t.Context(), auth, req, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			wire := <-captured
			if got := gjson.GetBytes(wire, `input.#(type=="reasoning").encrypted_content`).String(); got == "cpa:commentary:v1" {
				t.Errorf("internal commentary marker reached upstream encrypted_content: %s", wire)
			}
			commentary := gjson.GetBytes(wire, `input.#(phase=="commentary")`)
			if commentary.Get("type").String() != "message" || commentary.Get("role").String() != "assistant" || commentary.Get("content.0.text").String() != "COMMENTARY_SENTINEL" {
				t.Errorf("commentary did not become an assistant message: %s", wire)
			}
			for _, literal := range []string{"COMMENTARY_SENTINEL", "literal cpa:commentary:v1", "tool cpa:commentary:v1"} {
				if !bytes.Contains(wire, []byte(literal)) {
					t.Errorf("lost commentary or literal text %q: %s", literal, wire)
				}
			}
		})
	}
}
