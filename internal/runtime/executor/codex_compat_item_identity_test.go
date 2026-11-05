package executor

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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
				match := []config.PayloadModelRule{{Name: model}}
				cfg.Payload.Override = []config.PayloadRule{{Models: match, Params: map[string]any{"audit.calls.-1": "once"}}}
				cfg.Payload.OverrideRaw = []config.PayloadRule{{Models: match, Params: map[string]any{"audit.large": "9007199254740993"}}}
				auth := &cliproxyauth.Auth{ID: "compat-test-credential", Provider: "codex", ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic-key", "base_url": server.URL}}
				reasoningID := "provider_reasoning_" + strings.Repeat("a", 70)
				payload := []byte(fmt.Sprintf(`{"model":%q,"input":[{"type":"reasoning","id":%q,"summary":[],"content":[{"type":"reasoning_text","text":"provider reasoning"}]},{"type":"message","id":"provider_message","role":"assistant","content":[{"type":"output_text","text":"prior answer"}]},{"role":"user","content":"continue"}]}`, model, reasoningID))
				originalIDs := []string{"msg_short+id/", "msg_short_id_", "fc_" + strings.Repeat("界+/", 30), "fco_+855/"}
				payloadDoc := ojson.NewDoc(payload)
				for _, item := range []string{
					fmt.Sprintf(`{"type":"message","id":%q,"role":"assistant","content":[{"type":"output_text","text":"first"}]}`, originalIDs[0]),
					fmt.Sprintf(`{"type":"message","id":%q,"role":"assistant","content":[{"type":"output_text","text":"second"}]}`, originalIDs[1]),
					fmt.Sprintf(`{"type":"function_call","id":%q,"call_id":"call-855","name":"lookup","arguments":"{}"}`, originalIDs[2]),
					fmt.Sprintf(`{"type":"function_call_output","id":%q,"call_id":"call-855","output":"done"}`, originalIDs[3]),
				} {
					if errSet := payloadDoc.SetRaw("input.-1", item); errSet != nil {
						t.Fatal(errSet)
					}
				}
				payload = payloadDoc.Bytes()
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
				if ojson.GetBytes(body, "audit.calls.#").Int() != 1 || ojson.GetBytes(body, "audit.large").Raw != "9007199254740993" {
					t.Errorf("input sanitation changed final payload rules: %s", body)
				}
				seenIDs := make(map[string]bool)
				for i, original := range originalIDs {
					id := ojson.GetBytes(body, fmt.Sprintf("input.%d.id", i+3)).String()
					if compat {
						if id != original {
							t.Errorf("compat input.%d.id = %q, want original %q", i+3, id, original)
						}
					} else {
						invalid := strings.ContainsFunc(id, func(r rune) bool {
							return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
						})
						if id == "" || len(id) > 64 || invalid || seenIDs[id] {
							t.Errorf("native input.%d.id invalid or colliding: %q", i+3, id)
						}
						seenIDs[id] = true
					}
				}
				if ojson.GetBytes(body, "input.5.call_id").String() != "call-855" || ojson.GetBytes(body, "input.6.call_id").String() != "call-855" {
					t.Errorf("item ID sanitation changed tool call pairing: %s", body)
				}
				if compat {
					if got := ojson.GetBytes(body, "input.0.id").String(); got != reasoningID {
						t.Errorf("compat reasoning ID changed: got=%q want=%q", got, reasoningID)
					}
					if got := ojson.GetBytes(body, "input.1.id").String(); got != "provider_message" {
						t.Errorf("compat message ID changed: %q", got)
					}
					if got := ojson.GetBytes(body, "input.0.content.0.text").String(); got != "provider reasoning" {
						t.Errorf("compat reasoning content changed: %q", got)
					}
				} else {
					if ojson.GetBytes(body, "input.0.id").Exists() || len(ojson.GetBytes(body, "input.0.content").Array()) != 0 {
						t.Errorf("native reasoning sanitation changed: %s", body)
					}
					if got := ojson.GetBytes(body, "input.1.id").String(); got != "msg_provider_message" {
						t.Errorf("native message ID sanitation changed: %q", got)
					}
				}
			})
		}
	}
}
