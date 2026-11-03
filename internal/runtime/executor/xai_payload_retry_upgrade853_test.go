package executor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestUpgrade853XAIWebsocketSendRetryAppliesPayloadRulesOnce(t *testing.T) {
	var connections atomic.Int32
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		connections.Add(1)
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Logf("close fixture connection: %v", errClose)
			}
		}()
		_, body, errRead := conn.ReadMessage()
		if errRead != nil {
			return
		}
		captured <- body
		completed := []byte(`{"type":"response.completed","response":{"id":"retry-853","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Error(errWrite)
		}
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"store": false, "instructions": "configured"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"input.0"}}},
	}}
	executor := NewXAIWebsocketsExecutor(cfg)
	executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	const sessionID = "xai-payload-retry-853"
	t.Cleanup(func() { executor.CloseExecutionSession(sessionID) })
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/responses"
	conn, response, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	if response != nil && response.Body != nil {
		if errClose := response.Body.Close(); errClose != nil {
			t.Fatal(errClose)
		}
	}
	session := executor.getOrCreateSession(sessionID)
	session.conn = conn
	session.connCloser = newWebsocketConnectionCloser(conn)
	session.authID = "xai-retry-853"
	session.wsURL = wsURL
	// No reader observes this closure. The next executor write must fail and
	// exercise its reconnect-and-resend branch deterministically.
	session.readerConn = conn
	if errClose := session.connCloser.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	auth := &cliproxyauth.Auth{ID: session.authID, Provider: "xai", Attributes: map[string]string{"api_key": "synthetic-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"model":"grok-4.3","input":[{"type":"message","role":"user","content":"first"},{"type":"message","role":"user","content":"second"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID}}
	result, errExecute := executor.ExecuteStream(t.Context(), auth, req, opts)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	select {
	case body := <-captured:
		if connections.Load() != 2 || ojson.GetBytes(body, "input.#").Int() != 1 || !strings.Contains(ojson.GetBytes(body, "input.0").Raw, "second") {
			t.Fatalf("retry reapplied the input filter: connections=%d body=%s", connections.Load(), body)
		}
		if ojson.GetBytes(body, "store").Bool() || ojson.GetBytes(body, "instructions").String() != "configured" || ojson.GetBytes(body, "type").String() != "response.create" {
			t.Fatalf("retry overwrote the configured body or frame: %s", body)
		}
	default:
		t.Fatal("retry did not reach the fixture")
	}
}
