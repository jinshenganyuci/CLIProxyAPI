package executor

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexCompatPreservesProviderItemIDsOnWire(t *testing.T) {
	for _, mode := range []string{"http", "http_stream", "compact", "websocket", "websocket_stream"} {
		for _, compat := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/compat=%t", mode, compat), func(t *testing.T) {
				const model = "compat-test-model"
				const completed = `{"type":"response.completed","response":{"id":"resp_compat","object":"response","status":"completed","model":"compat-test-model","output":[{"id":"msg_answer","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if websocket.IsWebSocketUpgrade(request) {
						upgrader := websocket.Upgrader{}
						conn, errUpgrade := upgrader.Upgrade(writer, request, nil)
						if errUpgrade != nil {
							t.Errorf("upgrade: %v", errUpgrade)
							return
						}
						defer func() { _ = conn.Close() }()
						_, payload, errRead := conn.ReadMessage()
						if errRead != nil {
							t.Errorf("read WebSocket request: %v", errRead)
							return
						}
						captured <- payload
						if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(completed)); errWrite != nil {
							t.Errorf("write WebSocket response: %v", errWrite)
						}
						return
					}
					payload, errRead := io.ReadAll(request.Body)
					if errRead != nil {
						t.Errorf("read HTTP request: %v", errRead)
						return
					}
					captured <- payload
					if mode == "compact" {
						writer.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(writer, `{"id":"compact_1","object":"response.compaction","model":"compat-test-model","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
						return
					}
					writer.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(writer, "data: "+completed+"\n\n")
				}))
				defer server.Close()
				cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "synthetic-key", BaseURL: server.URL, Models: []config.CodexModel{{Name: model, IsCompat: compat}}}}}
				auth := &cliproxyauth.Auth{ID: "compat-test-credential", Provider: "codex", ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic-key", "base_url": server.URL}}
				reasoningID := "provider_reasoning_" + strings.Repeat("a", 70)
				payload := []byte(fmt.Sprintf(`{"model":%q,"input":[{"type":"reasoning","id":%q,"summary":[],"content":[{"type":"reasoning_text","text":"provider reasoning"}]},{"type":"message","id":"provider_message","role":"assistant","content":[{"type":"output_text","text":"prior answer"}]},{"role":"user","content":"continue"}]}`, model, reasoningID))
				req := cliproxyexecutor.Request{Model: model, Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				executor := NewCodexExecutor(cfg)
				websocketExecutor := NewCodexWebsocketsExecutor(cfg)
				websocketExecutor.store = &codexWebsocketSessionStore{}
				var errExecute error
				switch mode {
				case "http":
					_, errExecute = executor.Execute(t.Context(), auth, req, opts)
				case "compact":
					opts.Alt = "responses/compact"
					_, errExecute = executor.Execute(t.Context(), auth, req, opts)
				case "websocket":
					_, errExecute = websocketExecutor.Execute(t.Context(), auth, req, opts)
				default:
					opts.Stream = true
					var result *cliproxyexecutor.StreamResult
					if mode == "websocket_stream" {
						result, errExecute = websocketExecutor.ExecuteStream(t.Context(), auth, req, opts)
					} else {
						result, errExecute = executor.ExecuteStream(t.Context(), auth, req, opts)
					}
					if errExecute == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								errExecute = chunk.Err
								break
							}
						}
					}
				}
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				body := <-captured
				if compat {
					if got := gjson.GetBytes(body, "input.0.id").String(); got != reasoningID {
						t.Errorf("compat reasoning ID changed: got=%q want=%q", got, reasoningID)
					}
					if got := gjson.GetBytes(body, "input.1.id").String(); got != "provider_message" {
						t.Errorf("compat message ID changed: %q", got)
					}
					if got := gjson.GetBytes(body, "input.0.content.0.text").String(); got != "provider reasoning" {
						t.Errorf("compat reasoning content changed: %q", got)
					}
				} else {
					if gjson.GetBytes(body, "input.0.id").Exists() || len(gjson.GetBytes(body, "input.0.content").Array()) != 0 {
						t.Errorf("native reasoning sanitation changed: %s", body)
					}
					if got := gjson.GetBytes(body, "input.1.id").String(); got != "msg_provider_message" {
						t.Errorf("native message ID sanitation changed: %q", got)
					}
				}
			})
		}
	}
}
