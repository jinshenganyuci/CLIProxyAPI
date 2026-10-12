package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestChatToClaudeTerminalRequiresCleanTransportUpgrade823(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprintf("truncated=%t", truncated), func(t *testing.T) {
			frames := "data: " + `{"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"observed"}}]}` + "\n\n" +
				"data: " + `{"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}` + "\n\n"
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if truncated {
					w.Header().Set("Content-Length", fmt.Sprint(len(frames)+1024))
				}
				_, _ = io.WriteString(w, frames)
			}))
			defer origin.Close()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "compat823", ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": origin.URL}}
			capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			exec := NewOpenAICompatExecutor("compat823", &config.Config{})
			result, errStart := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "model", Payload: []byte(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatClaude, Stream: true})
			if errStart != nil {
				t.Fatal(errStart)
			}
			var output strings.Builder
			var streamErr error
			for chunk := range result.Chunks {
				output.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if (streamErr != nil) != truncated {
				t.Fatalf("stream error=%v, truncated=%t", streamErr, truncated)
			}
			deltas, stops := 0, 0
			for _, line := range strings.Split(output.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				payload := strings.TrimPrefix(line, "data: ")
				switch gjson.Get(payload, "type").String() {
				case "message_delta":
					deltas++
					if gjson.Get(payload, "usage.output_tokens").Int() != 20 {
						t.Fatalf("clean EOF lost final output usage: %s", payload)
					}
				case "message_stop":
					stops++
				}
			}
			want := 1
			if truncated {
				want = 0
			}
			if deltas != want || stops != want {
				t.Fatalf("terminal delta/stop=%d/%d, want %d/%d", deltas, stops, want, want)
			}
			records := providerV810UsageRecords(t, capture)
			if len(records) != 1 || records[0].Failed != truncated || records[0].Detail.TotalTokens != 120 {
				t.Fatalf("transport result lost observed accounting: %+v", records)
			}
		})
	}
}
