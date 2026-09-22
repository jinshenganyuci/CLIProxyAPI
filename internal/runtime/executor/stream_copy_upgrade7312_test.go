package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestStreamCopyUpgrade7312RetainedChunksKeepWireBytes(t *testing.T) {
	for _, provider := range []string{"gemini-interactions", "kimi"} {
		t.Run(provider, func(t *testing.T) {
			var wire strings.Builder
			for i := range 256 {
				if provider == "kimi" {
					fmt.Fprintf(&wire, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"%03d <keep>& \\u263a %s\"}\n\n", i, strings.Repeat("payload", 32))
				} else {
					fmt.Fprintf(&wire, "event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"%03d <keep>& \\u263a %s\"}}\n\n", i, strings.Repeat("payload", 32))
				}
			}
			if provider == "kimi" {
				wire.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"done\",\"status\":\"completed\"}}\n\n")
			} else {
				wire.WriteString("event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"done\",\"status\":\"completed\"}}\n\n")
			}
			want := wire.String()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, want)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			auth := &cliproxyauth.Auth{
				Provider:   provider,
				Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic-test-key"},
				Metadata:   map[string]any{"access_token": "synthetic-test-token"},
			}
			req := cliproxyexecutor.Request{Model: "gemini-3.1-flash-lite", Payload: []byte(`{"input":"hello"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions, ResponseFormat: sdktranslator.FormatInteractions, Stream: true}
			var result *cliproxyexecutor.StreamResult
			var errExecute error
			if provider == "kimi" {
				req.Model = "kimi-k3"
				opts.SourceFormat = sdktranslator.FormatOpenAIResponse
				opts.ResponseFormat = sdktranslator.FormatOpenAIResponse
				result, errExecute = NewKimiExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, opts)
			} else {
				result, errExecute = NewGeminiInteractionsExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, opts)
			}
			if errExecute != nil {
				t.Fatalf("execute stream: %v", errExecute)
			}
			var retained [][]byte
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk: %v", chunk.Err)
				}
				// Keep the original slices until all scanner reads and frame reuse finish.
				retained = append(retained, chunk.Payload)
			}
			if got := bytes.Join(retained, nil); string(got) != want {
				t.Fatalf("retained stream bytes changed: got %d bytes, want %d", len(got), len(want))
			}
			if string(req.Payload) != `{"input":"hello"}` {
				t.Fatalf("stream execution mutated request: %s", req.Payload)
			}
		})
	}
}
