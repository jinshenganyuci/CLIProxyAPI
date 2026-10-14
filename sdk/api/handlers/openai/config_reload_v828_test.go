package openai

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestV828WebsocketReloadKeepsCredentialBoundInterruptAndNextTurn(t *testing.T) {
	for _, mode := range []string{"http-fallback", "serial-websocket", "duplex-websocket"} {
		t.Run(mode, func(t *testing.T) {
			const turns = 12
			var requests, connections atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "http-fallback" {
					_, _ = io.Copy(io.Discard, r.Body)
					id := fmt.Sprintf("response-%d", requests.Add(1))
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":%q}}\n\n", id)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				connections.Add(1)
				conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				for {
					_, create, errRead := conn.ReadMessage()
					if errRead != nil {
						return
					}
					if gjson.GetBytes(create, "type").String() != "response.create" {
						t.Errorf("expected next create, got %s", create)
						return
					}
					id := fmt.Sprintf("response-%d", requests.Add(1))
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id))); errWrite != nil {
						return
					}
					_, control, errRead := conn.ReadMessage()
					if errRead != nil || gjson.GetBytes(control, "type").String() != "response.interrupt" || gjson.GetBytes(control, "response_id").String() != id {
						t.Errorf("interrupt changed across reload: %s %v", control, errRead)
						return
					}
					ack := fmt.Sprintf(`{"type":"response.incomplete","response":{"id":%q,"status":"incomplete","incomplete_details":{"reason":"interrupted"},"output":[],"usage":{"output_tokens":1}}}`, id)
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(ack)); errWrite != nil {
						return
					}
				}
			}))
			t.Cleanup(upstream.Close)
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = mode == "duplex-websocket"
			cfg.CodexResponseSteering = cfg.Codex.ResponseSteering
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			id, model := t.Name(), "v828-reload-model"
			_, errRegister := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic-v828", "base_url": upstream.URL, "websockets": fmt.Sprint(mode != "http-fallback")}})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			router := gin.New()
			router.GET("/v1/responses", NewOpenAIResponsesAPIHandler(base).ResponsesWebsocket)
			gateway := httptest.NewServer(router)
			t.Cleanup(gateway.Close)
			client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses", nil)
			if errDial != nil {
				t.Fatal(errDial)
			}
			t.Cleanup(func() { _ = client.Close() })
			_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
			stopReload, reloadDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(reloadDone)
				for n := 0; ; n++ {
					select {
					case <-stopReload:
						return
					default:
					}
					snapshot := cfg.SDKConfig
					// A credential's explicit route remains authoritative over live global edits.
					snapshot.ProxyURL = fmt.Sprintf("http://127.0.0.1:%d", 1+n%2)
					base.UpdateClients(&snapshot)
					runtime.Gosched()
				}
			}()
			defer func() { close(stopReload); <-reloadDone }()
			read := func(kind, id string) {
				t.Helper()
				_, payload, errRead := client.ReadMessage()
				if errRead != nil || gjson.GetBytes(payload, "type").String() != kind || gjson.GetBytes(payload, "response.id").String() != id {
					t.Fatalf("reload disrupted response %s/%s: %s %v", kind, id, payload, errRead)
				}
			}
			for turn := 1; turn <= turns; turn++ {
				if errWrite := client.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model))); errWrite != nil {
					t.Fatal(errWrite)
				}
				responseID := fmt.Sprintf("response-%d", turn)
				read("response.created", responseID)
				if errWrite := client.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.interrupt","response_id":%q}`, responseID))); errWrite != nil {
					t.Fatal(errWrite)
				}
				read("response.incomplete", responseID)
			}
			if requests.Load() != turns || (mode != "http-fallback" && connections.Load() != 1) {
				t.Fatalf("reload rotated requests or upstream sockets: requests=%d connections=%d", requests.Load(), connections.Load())
			}
		})
	}
}
