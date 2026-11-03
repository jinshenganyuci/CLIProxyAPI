package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestUpgrade851MultilineCodexCommentaryKeepsDisplayPolicy(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "")
	// The fork's complete-event scanner must feed the new commentary policy one
	// complete JSON event, including when bootstrap buffering releases the stream.
	wire := "data: {\"type\":\"response.created\",\ndata: \"response\":{\"id\":\"r851\",\"model\":\"gpt-5.4\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\ndata: \"item\":{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"commentary\",\"content\":[{\"type\":\"output_text\",\"text\":\"Checking files.\"}]}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\ndata: \"item\":{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":\"Done.\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"r851\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":7}}}\n\n"
	for _, buffered := range []bool{false, true} {
		for _, visible := range []bool{false, true} {
			t.Run(fmt.Sprintf("buffered=%t/visible=%t", buffered, visible), func(t *testing.T) {
				cfg := &config.Config{}
				cfg.Codex.StreamBootstrapBuffering = buffered
				cfg.Client.Claude.ShowCodexCommentary = visible
				executor := NewCodexExecutor(cfg)
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: wire})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
				req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Inspect files."}]}`)}
				result, err := executor.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatClaude})
				if err != nil {
					t.Fatal(err)
				}
				var text, thinking strings.Builder
				var markerCount, outputTokens int64
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					for _, line := range strings.Split(string(chunk.Payload), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						event := ojson.Parse(strings.TrimPrefix(line, "data: "))
						switch event.Get("delta.type").String() {
						case "text_delta":
							text.WriteString(event.Get("delta.text").String())
						case "thinking_delta":
							thinking.WriteString(event.Get("delta.thinking").String())
						case "signature_delta":
							if event.Get("delta.signature").String() == "cpa:commentary:v1" {
								markerCount++
							}
						}
						if event.Get("type").String() == "message_delta" {
							outputTokens = event.Get("usage.output_tokens").Int()
						}
					}
				}
				wantText := "Done."
				if visible {
					wantText = "Checking files.Done."
				}
				if text.String() != wantText || thinking.String() != "Checking files." || markerCount != 1 || outputTokens != 7 {
					t.Fatalf("multiline translation changed commentary, replay marker, or usage: text=%q thinking=%q markers=%d tokens=%d", text.String(), thinking.String(), markerCount, outputTokens)
				}
			})
		}
	}
}
