package executor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexCredentialIdentityEmptyStreamRestoresHeadersV808(t *testing.T) {
	for _, buffering := range []bool{false, true} {
		name := "unbuffered"
		if buffering {
			name = "buffered"
		}
		t.Run(name, func(t *testing.T) {
			captured := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				session := codexSessionHeaderValue(r.Header)
				captured <- session
				w.Header().Set("Session-Id", session)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
			}))
			t.Cleanup(upstream.Close)
			cfg := codexCredentialIdentityTestConfig(false)
			cfg.Codex.StreamBootstrapBuffering = buffering
			auth := codexCredentialIdentityTestAuth("codex-empty-stream.json", "db67185c-e043-4701-863a-e795b245ee31")
			auth.Attributes = map[string]string{"base_url": upstream.URL}
			auth.ProxyURL = "direct"
			result, errStream := NewCodexExecutor(cfg).ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5-codex",
				Payload: []byte(`{"model":"gpt-5-codex","prompt_cache_key":"client-empty-session","input":"hello"}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse})
			if errStream != nil {
				t.Fatal(errStream)
			}
			if result == nil || result.Chunks == nil {
				t.Fatal("empty upstream must return a closed stream for conductor failover")
			}
			for chunk := range result.Chunks {
				t.Fatalf("empty upstream produced a chunk: %+v", chunk)
			}
			if got := result.Headers.Get("Session-Id"); got != "client-empty-session" {
				t.Fatalf("empty-stream header = %q, want original client identity", got)
			}
			if got := <-captured; got == "" || got == "client-empty-session" {
				t.Fatalf("credential identity was not applied upstream: %q", got)
			}
		})
	}
}
