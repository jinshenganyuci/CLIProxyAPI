package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type providerV810UsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *providerV810UsageCapture) HandleUsage(_ context.Context, r usage.Record) {
	if r.AuthID == c.authID {
		c.records <- r
	}
}

// The marker traverses the same FIFO worker after the stream closes, avoiding
// timer-based assertions about whether another usage record might arrive.
func providerV810UsageRecords(t *testing.T, capture *providerV810UsageCapture) []usage.Record {
	t.Helper()
	usage.PublishRecord(context.Background(), usage.Record{AuthID: capture.authID, Model: "usage-barrier"})
	var records []usage.Record
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case r := <-capture.records:
			if r.Model == "usage-barrier" {
				return records
			}
			records = append(records, r)
		case <-deadline.C:
			t.Fatal("usage worker did not reach barrier")
			return nil
		}
	}
}

func TestProviderV810StreamUsageSurvivesTermination(t *testing.T) {
	for _, provider := range []string{"gemini", "gemini-interactions", "vertex", "antigravity"} {
		t.Run(provider, func(t *testing.T) {
			for _, ending := range []string{"complete", "truncated", "cancel"} {
				t.Run(ending, func(t *testing.T) {
					authID := t.Name()
					capture := &providerV810UsageCapture{authID: authID, records: make(chan usage.Record, 8)}
					usage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
					payload := `{"candidates":[{"content":{"role":"model","parts":[{"text":"observed"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}`
					// Native accepts cancellation after a delivered terminal for these providers;
					// retain coverage of cancellation that interrupts partial output here.
					if ending == "cancel" && (provider == "antigravity" || provider == "vertex" || provider == "gemini") {
						payload = strings.Replace(payload, `,"finishReason":"STOP"`, "", 1)
					}
					if provider == "antigravity" {
						payload = `{"response":` + payload + `}`
					}
					if provider == "gemini-interactions" {
						payload = `{"event_type":"interaction.complete","interaction":{"outputs":[{"type":"text","text":"observed"}],"usage":{"total_input_tokens":11,"total_output_tokens":22,"total_tokens":33}}}`
					}
					frame := "data: " + payload + "\n\n"
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						if ending == "truncated" {
							w.Header().Set("Content-Length", fmt.Sprint(len(frame)+1024))
						}
						_, _ = io.WriteString(w, frame)
						w.(http.Flusher).Flush()
						if ending == "cancel" {
							<-r.Context().Done()
						}
					}))
					defer server.Close()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://127.0.0.1:1"}, RequestRetry: 1}
					auth := &cliproxyauth.Auth{ID: authID, Provider: provider, ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": server.URL}, Metadata: map[string]any{"access_token": "synthetic", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "synthetic"}}
					req := cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Stream: true}
					var result *cliproxyexecutor.StreamResult
					var err error
					switch provider {
					case "gemini":
						result, err = NewGeminiExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
					case "gemini-interactions":
						opts.SourceFormat = sdktranslator.FormatInteractions
						opts.ResponseFormat = sdktranslator.FormatInteractions
						req.Payload = []byte(`{"input":"hello"}`)
						result, err = NewGeminiInteractionsExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
					case "vertex":
						result, err = NewGeminiVertexExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
					case "antigravity":
						result, err = NewAntigravityExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
					}
					if err != nil {
						t.Fatal(err)
					}
					observed := false
					receivedError := false
					deadline := time.NewTimer(5 * time.Second)
					defer deadline.Stop()
					done := false
					for !done {
						select {
						case chunk, ok := <-result.Chunks:
							if !ok {
								done = true
								break
							}
							if chunk.Err != nil {
								receivedError = true
							}
							if strings.Contains(string(chunk.Payload), "observed") {
								observed = true
								if ending == "cancel" {
									cancel()
								}
							}
						case <-deadline.C:
							t.Fatal("stream did not terminate")
						}
					}
					if !observed {
						t.Fatal("usage-bearing payload was not delivered")
					}
					if ending == "truncated" && !receivedError {
						t.Fatal("truncated HTTP body did not report an error")
					}
					var records []usage.Record
					if provider == "antigravity" {
						records = antigravityUsageRecordsAfterDelivery815(t, capture)
					} else {
						records = providerV810UsageRecords(t, capture)
					}
					if len(records) != 1 {
						t.Fatalf("usage records=%d, want exactly one", len(records))
					}
					record := records[0]
					if record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 22 || record.Detail.TotalTokens != 33 {
						t.Errorf("usage=%+v, want input 11 output 22 total 33", record.Detail)
					}
					if record.Failed != (ending != "complete") {
						t.Errorf("failed=%v for %s", record.Failed, ending)
					}
				})
			}
		})
	}
}

// Validation must publish the complete failed-attempt detail before attempting
// to send the failure frame to a downstream that is not reading.
func TestProviderV810PatchFailureAccountsBeforeBackpressure(t *testing.T) {
	for _, provider := range []string{"gemini", "vertex", "antigravity", "xai", "xai-websocket"} {
		t.Run(provider, func(t *testing.T) {
			authID := t.Name()
			capture := &providerV810UsageCapture{authID: authID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			response := `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"apply_patch","args":{"input":{"invalid":"synthetic"}}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}`
			if provider == "antigravity" {
				response = `{"response":` + response + `}`
			}
			if strings.HasPrefix(provider, "xai") {
				response = `{"type":"response.completed","response":{"id":"synthetic-response","status":"completed","output":[{"type":"function_call","id":"synthetic-item","call_id":"synthetic-call","name":"apply_patch","arguments":"{\"input\":{\"invalid\":\"synthetic\"}}"}],"usage":{"input_tokens":11,"output_tokens":22,"total_tokens":33}}`
			}
			socketClosed := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if provider == "xai-websocket" {
					conn, errUpgrade := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
					if errUpgrade != nil {
						return
					}
					defer func() {
						if errClose := conn.Close(); errClose != nil {
							t.Logf("close test websocket: %v", errClose)
						}
					}()
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						return
					}
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(response)); errWrite != nil {
						return
					}
					_, _, _ = conn.ReadMessage()
					close(socketClosed)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+response+"\n\n")
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://127.0.0.1:1"}, RequestRetry: 1}
			auth := &cliproxyauth.Auth{ID: authID, Provider: provider, ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": server.URL, "websockets": "true"}, Metadata: map[string]any{"access_token": "synthetic", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "synthetic"}}
			req := cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"input":"patch a file","tools":[{"type":"custom","name":"apply_patch"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true, OriginalRequest: req.Payload}
			var result *cliproxyexecutor.StreamResult
			var err error
			switch provider {
			case "gemini":
				result, err = NewGeminiExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
			case "vertex":
				result, err = NewGeminiVertexExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
			case "antigravity":
				result, err = NewAntigravityExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
			case "xai":
				req.Model = "grok-4"
				result, err = NewXAIExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
			case "xai-websocket":
				req.Model = "grok-4"
				exec := NewXAIWebsocketsExecutor(cfg)
				exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()}
				result, err = exec.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(ctx), auth, req, opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			var record usage.Record
			select {
			case record = <-capture.records:
			case <-time.After(5 * time.Second):
				cancel()
				for range result.Chunks {
				}
				t.Fatal("validation usage blocked behind downstream output")
			}
			if !record.Failed || record.Fail.StatusCode != http.StatusBadGateway || record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 22 || record.Detail.TotalTokens != 33 {
				t.Errorf("failed attempt lost usage before delivery: %+v", record)
			}
			if provider == "xai-websocket" {
				select {
				case <-socketClosed:
				case <-time.After(5 * time.Second):
					t.Error("invalid websocket attempt retained its upstream connection")
				}
			}
			cancel()
			for range result.Chunks {
			}
			if records := providerV810UsageRecords(t, capture); len(records) != 0 {
				t.Fatalf("failure was published again during cancellation: %+v", records)
			}
		})
	}
}

func TestAIStudioV810StreamUsageSurvivesRelayTermination(t *testing.T) {
	for _, ending := range []string{"complete", "relay-error", "cancel", "invalid-http", "late-http-error", "split-complete", "split-relay-error"} {
		t.Run(ending, func(t *testing.T) {
			authID := t.Name()
			capture := &providerV810UsageCapture{authID: authID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			connected := make(chan struct{})
			relay := wsrelay.NewManager(wsrelay.Options{ProviderFactory: func(*http.Request) (string, error) { return authID, nil }, OnConnected: func(string) { close(connected) }})
			server := httptest.NewServer(relay.Handler())
			defer server.Close()
			defer func() {
				if errStop := relay.Stop(context.Background()); errStop != nil {
					t.Error(errStop)
				}
			}()
			conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
			if errDial != nil {
				t.Fatal(errDial)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Logf("close test relay: %v", errClose)
				}
			}()
			<-connected
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clientDone := make(chan error, 1)
			go func() {
				var request wsrelay.Message
				if errRead := conn.ReadJSON(&request); errRead != nil {
					clientDone <- errRead
					return
				}
				response := `{"candidates":[{"content":{"role":"model","parts":[{"text":"observed"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}`
				events := []wsrelay.Message{{ID: request.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}}, {ID: request.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": "data: " + response}}}
				if ending == "split-complete" || ending == "split-relay-error" {
					withoutUsage := strings.Replace(response, `,"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}`, "", 1)
					events[1].Payload = map[string]any{"data": "data: " + withoutUsage}
					events = append(events, wsrelay.Message{ID: request.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": `data: {"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}`}})
					if ending == "split-complete" {
						events = append(events, wsrelay.Message{ID: request.ID, Type: wsrelay.MessageTypeStreamEnd})
					} else {
						events = append(events, wsrelay.Message{ID: request.ID, Type: wsrelay.MessageTypeError, Payload: map[string]any{"message": "synthetic split relay error"}})
					}
				}
				if ending == "invalid-http" {
					response = strings.ReplaceAll(response, `{"text":"observed"}`, `{"functionCall":{"name":"apply_patch","args":{"input":{"invalid":"synthetic"}}}}`)
					events = []wsrelay.Message{{ID: request.ID, Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": 200, "body": response}}}
				} else if ending == "complete" {
					events = append(events, wsrelay.Message{ID: request.ID, Type: wsrelay.MessageTypeStreamEnd})
				} else if ending == "relay-error" {
					events = append(events, wsrelay.Message{ID: request.ID, Type: wsrelay.MessageTypeError, Payload: map[string]any{"message": "synthetic relay error"}})
				} else if ending == "late-http-error" {
					events = append(events, wsrelay.Message{ID: request.ID, Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": http.StatusServiceUnavailable, "body": `{"error":{"message":"synthetic late failure"}}`}})
				}
				for _, event := range events {
					if errWrite := conn.WriteJSON(event); errWrite != nil {
						clientDone <- errWrite
						return
					}
				}
				clientDone <- nil
			}()
			req := cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Stream: true}
			if ending == "invalid-http" {
				req.Payload = []byte(`{"input":"patch a file","tools":[{"type":"custom","name":"apply_patch"}]}`)
				opts.SourceFormat = sdktranslator.FormatOpenAIResponse
				opts.ResponseFormat = sdktranslator.FormatOpenAIResponse
				opts.OriginalRequest = req.Payload
			}
			result, errExecute := NewAIStudioExecutor(&config.Config{}, "aistudio", relay).ExecuteStream(ctx, &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}, req, opts)
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			var first *usage.Record
			if ending == "invalid-http" {
				select {
				case record := <-capture.records:
					first = &record
					cancel()
				case <-time.After(5 * time.Second):
					cancel()
					for range result.Chunks {
					}
					t.Fatal("invalid HTTP response usage blocked behind output")
				}
			}
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			done := false
			for !done {
				select {
				case chunk, ok := <-result.Chunks:
					if !ok {
						done = true
						break
					}
					if ending == "cancel" && strings.Contains(string(chunk.Payload), "observed") {
						cancel()
					}
				case <-deadline.C:
					t.Fatal("relay stream did not terminate")
				}
			}
			if errClient := <-clientDone; errClient != nil {
				t.Fatal(errClient)
			}
			records := providerV810UsageRecords(t, capture)
			if first != nil {
				records = append([]usage.Record{*first}, records...)
			}
			if len(records) != 1 {
				t.Fatalf("usage records=%d, want one", len(records))
			}
			record := records[0]
			if record.Failed != (ending != "complete" && ending != "split-complete") || record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 22 || record.Detail.TotalTokens != 33 {
				t.Fatalf("relay ending %s usage=%+v", ending, record)
			}
		})
	}
}
