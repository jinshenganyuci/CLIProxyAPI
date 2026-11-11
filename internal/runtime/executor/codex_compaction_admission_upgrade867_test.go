package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestV867CodexDuplexLocalCompactionKeepsAdmissionAndCredentialFailure(t *testing.T) {
	for _, outcome := range []string{"success", "admission-denied", "upstream-forbidden"} {
		t.Run(outcome, func(t *testing.T) {
			var occupied, acquisitions, releases, httpCalls atomic.Int32
			occupied.Store(1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if websocket.IsWebSocketUpgrade(r) {
					conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Error(errUpgrade)
						return
					}
					defer func() { _ = conn.Close() }()
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						return
					}
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_initial","output":[]}}`))
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_initial","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
					_, _, _ = conn.ReadMessage()
					return
				}
				httpCalls.Add(1)
				if occupied.Load() != 1 {
					t.Error("HTTP summary bypassed the credential admission reservation")
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-compat-key" {
					t.Error("HTTP summary changed the bound credential")
				}
				body, _ := io.ReadAll(r.Body)
				if bytes.Contains(body, []byte("compaction_trigger")) || ojson.GetBytes(body, "tools").Exists() {
					t.Error("HTTP summary bypassed compaction request preparation")
				}
				if outcome == "upstream-forbidden" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"error":{"type":"permission_error","message":"synthetic credential denied"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp_summary","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"A retained summary."}]}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`+"\n\n")
			}))
			t.Cleanup(upstream.Close)
			cfg := &config.Config{Codex: config.CodexConfig{ResponseSteering: true}, CodexKey: []config.CodexKey{{
				APIKey: "synthetic-compat-key", BaseURL: upstream.URL,
				Models: []config.CodexModel{{Name: "compat-model", IsCompat: true}},
			}}}
			// Both the websocket and the internal HTTP summary must honor direct
			// on the selected credential instead of inheriting this invalid route.
			cfg.ProxyURL = "http://127.0.0.1:1"
			executor := NewCodexWebsocketsExecutor(cfg)
			executor.store = &codexWebsocketSessionStore{}
			credential := &auth.Auth{ID: "compaction-admission", Provider: "codex", ProxyURL: "direct", Attributes: map[string]string{
				"api_key": "synthetic-compat-key", "base_url": upstream.URL, "websockets": "true",
			}}
			ctx, cancel := context.WithCancel(t.Context())
			const sessionID = "compaction-admission-v867"
			t.Cleanup(func() { cancel(); executor.CloseExecutionSession(sessionID) })
			input := make(chan core.WebsocketInput, 1)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			ctx = core.WithWebsocketResponseAdmission(ctx, &core.WebsocketResponseAdmission{
				ReleaseInitial: func() { occupied.Add(-1) },
				Acquire: func(context.Context) (func(), error) {
					acquisitions.Add(1)
					if outcome == "admission-denied" {
						return nil, &auth.Error{Code: "credential_concurrency_exceeded", HTTPStatus: http.StatusTooManyRequests, Message: "synthetic busy credential"}
					}
					occupied.Add(1)
					var once sync.Once
					return func() { once.Do(func() { occupied.Add(-1); releases.Add(1) }) }, nil
				},
			})
			stream, errExecute := executor.ExecuteStream(ctx, credential, core.Request{
				Model: "compat-model", Payload: []byte(`{"type":"response.create","model":"compat-model","input":"initial"}`),
			}, core.Options{SourceFormat: translator.FormatOpenAIResponse, ResponseFormat: translator.FormatOpenAIResponse, Stream: true,
				Metadata: map[string]any{core.ExecutionSessionMetadataKey: sessionID}})
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			readEvent := func(want string) (ojson.Result, error) {
				t.Helper()
				deadline := time.After(5 * time.Second)
				for {
					select {
					case chunk, ok := <-stream.Chunks:
						if !ok {
							t.Fatalf("stream closed before %s", want)
						}
						if chunk.Err != nil {
							return ojson.Result{}, chunk.Err
						}
						for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
							line = bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(line), []byte("data:")))
							event := ojson.ParseBytes(line)
							if event.Get("type").String() == want {
								return event, nil
							}
						}
					case <-deadline:
						t.Fatalf("timed out waiting for %s", want)
					}
				}
			}
			if _, errRead := readEvent("response.completed"); errRead != nil || occupied.Load() != 0 {
				t.Fatalf("initial response did not release capacity: %v, occupied=%d", errRead, occupied.Load())
			}
			input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","event_id":"summary-turn","model":"compat-model","input":[{"type":"message","role":"user","content":"Remember this"},{"type":"compaction_trigger"}]}`)}
			want := "response.completed"
			if outcome == "admission-denied" {
				want = "error"
			}
			event, errRead := readEvent(want)
			if outcome == "upstream-forbidden" {
				var status interface{ StatusCode() int }
				var scoped interface{ IsRequestScoped() bool }
				if !errors.As(errRead, &status) || status.StatusCode() != http.StatusForbidden || (errors.As(errRead, &scoped) && scoped.IsRequestScoped()) {
					t.Fatalf("HTTP credential rejection lost classification: %v", errRead)
				}
			} else if errRead != nil {
				t.Fatal(errRead)
			}
			if outcome == "admission-denied" {
				if httpCalls.Load() != 0 || releases.Load() != 0 || event.Get("error.code").String() != "credential_concurrency_exceeded" {
					t.Fatal("local admission rejection reached upstream or lost its code")
				}
			} else if httpCalls.Load() != 1 || releases.Load() != 1 {
				t.Fatalf("summary calls/releases=%d/%d, want 1/1", httpCalls.Load(), releases.Load())
			}
			if occupied.Load() != 0 || acquisitions.Load() != 1 {
				t.Fatalf("summary leaked or bypassed capacity: occupied=%d acquisitions=%d", occupied.Load(), acquisitions.Load())
			}
		})
	}
}
