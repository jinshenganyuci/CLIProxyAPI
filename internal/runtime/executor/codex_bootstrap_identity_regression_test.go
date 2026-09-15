package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// The native budget branches must preserve the credential identity mapping on both
// a retryable synchronous rejection and a rejection delivered after buffering ends.
func TestCodexBootstrapCredentialIdentityErrorBudget(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, elapsed := range []time.Duration{0, 11 * time.Second} {
			for _, statusBearing := range []bool{false, true} {
				if statusBearing && transport == "http" {
					continue
				}
				t.Run(fmt.Sprintf("%s/elapsed=%s/status-bearing=%t", transport, elapsed, statusBearing), func(t *testing.T) {
					var calls atomic.Uint64
					start := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
					t.Cleanup(setCodexBootstrapNowForTest(func() time.Time {
						if calls.Add(1) == 1 {
							return start
						}
						return start.Add(elapsed)
					}))
					server, captured := codexBootstrapIdentityServer(t, transport, func(session string) []string {
						event := fmt.Sprintf(`{"type":"response.failed","response":{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","session_id":%q,"note":%q}}}`, session, session)
						if statusBearing {
							event = fmt.Sprintf(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"quota exhausted","resets_in_seconds":3600,"session_id":%q,"note":%q},"headers":{"Session-Id":%q,"Retry-After":"3600"}}`, session, session, session)
						}
						if elapsed > 0 {
							// The error itself is the first message after the budget expires.
							return []string{event}
						}
						return []string{codexCreatedEvent, codexKeepaliveEvent, codexOutputAddedEvent, event}
					})
					defer server.Close()
					result, err := codexExecuteBootstrapIdentity(t, transport, server.URL)
					mappedSession := <-captured
					if mappedSession == "" || mappedSession == "client-session" {
						t.Fatalf("upstream session was not credential mapped: %q", mappedSession)
					}
					if elapsed == 0 {
						if result != nil || err == nil {
							t.Fatalf("pre-budget rejection must fail before releasing any handshake: result=%v err=%v", result, err)
						}
					} else {
						if err != nil || result == nil {
							t.Fatalf("post-budget rejection must be delivered in-stream: result=%v err=%v", result, err)
						}
						_, err = drainChunks(result)
						if err == nil {
							t.Fatal("missing post-budget stream error")
						}
					}
					if got := gjson.Get(err.Error(), "error.session_id").String(); got != "client-session" {
						t.Errorf("client error identity = %q; error=%v", got, err)
					}
					if got := gjson.Get(err.Error(), "error.note").String(); got != mappedSession {
						t.Errorf("unrelated error value was changed: %q", got)
					}
					if statusBearing {
						metadata, ok := err.(interface {
							StatusCode() int
							IsCredentialScoped() bool
							RetryAfter() *time.Duration
							Headers() http.Header
						})
						if !ok {
							t.Fatalf("status-bearing error lost metadata: %T", err)
						}
						if metadata.StatusCode() != http.StatusTooManyRequests || !metadata.IsCredentialScoped() {
							t.Errorf("quota error status/scope changed: %v", err)
						}
						if retry := metadata.RetryAfter(); retry == nil || *retry != time.Hour {
							t.Errorf("quota retry duration = %v, want one hour", retry)
						}
						if metadata.Headers().Get("Session-Id") != "client-session" || metadata.Headers().Get("Retry-After") != "3600" {
							t.Errorf("quota headers lost identity or retry data: %v", metadata.Headers())
						}
					} else if elapsed == 0 && statusCodeFromTestError(t, err) != http.StatusServiceUnavailable {
						t.Errorf("bootstrap overload status changed: %v", err)
					}
				})
			}
		}
	}
}

func TestCodexNativeBootstrapIdentityPreservesAnnouncementsAndCompletion(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			const emptyPart = `{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""},"future":{"ok":true}}`
			completed := func(session string) string {
				return fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"session_id":%q,"future":{"ok":true},"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, session)
			}
			server, captured := codexBootstrapIdentityServer(t, transport, func(session string) []string {
				return []string{codexCreatedEvent, codexKeepaliveEvent, codexOutputAddedEvent, emptyPart,
					`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`, completed(session)}
			})
			defer server.Close()
			result, err := codexExecuteBootstrapIdentity(t, transport, server.URL)
			if err != nil || result == nil {
				t.Fatalf("native stream failed: result=%v err=%v", result, err)
			}
			combined, streamErr := drainChunks(result)
			if streamErr != nil {
				t.Fatal(streamErr)
			}
			if mapped := <-captured; mapped == "" || mapped == "client-session" {
				t.Fatalf("upstream identity was not mapped: %q", mapped)
			}
			wantEvents := []string{"response.created", "keepalive", "response.output_item.added", "response.content_part.added", "response.output_item.done", "response.completed"}
			var events []string
			for _, line := range bytes.Split([]byte(combined), []byte("\n")) {
				payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				eventType := gjson.GetBytes(payload, "type").String()
				if eventType == "" {
					continue
				}
				events = append(events, eventType)
				if eventType == "response.content_part.added" && string(payload) != emptyPart {
					t.Errorf("empty announcement lost native fields: %s", payload)
				}
				if eventType == "response.completed" {
					var got, want any
					if errJSON := json.Unmarshal(payload, &got); errJSON != nil {
						t.Fatal(errJSON)
					}
					if errJSON := json.Unmarshal([]byte(completed("client-session")), &want); errJSON != nil {
						t.Fatal(errJSON)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("native completion was backfilled or lost identity/unknown fields: %s", payload)
					}
				}
			}
			if fmt.Sprint(events) != fmt.Sprint(wantEvents) {
				t.Errorf("event order/count changed: got %v, want %v", events, wantEvents)
			}
		})
	}
}

func codexBootstrapIdentityServer(t *testing.T, transport string, events func(string) []string) (*httptest.Server, <-chan string) {
	t.Helper()
	captured := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := codexSessionHeaderValue(r.Header)
		captured <- session
		if transport == "websocket" {
			upgrader := websocket.Upgrader{}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close() }()
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				t.Error(errRead)
				return
			}
			for _, event := range events(session) {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
					t.Error(errWrite)
					return
				}
			}
			return
		}
		if _, errRead := io.Copy(io.Discard, r.Body); errRead != nil {
			t.Error(errRead)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events(session) {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	return server, captured
}

func codexExecuteBootstrapIdentity(t *testing.T, transport, baseURL string) (*cliproxyexecutor.StreamResult, error) {
	t.Helper()
	cfg := codexCredentialIdentityTestConfig(false)
	cfg.Codex.StreamBootstrapBuffering = true
	cfg.Codex.StreamBootstrapTimeout = "10s"
	auth := codexCredentialIdentityTestAuth("bootstrap-native.json", "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
	auth.Attributes = map[string]string{"base_url": baseURL}
	auth.ProxyURL = "direct"
	execute := NewCodexExecutor(cfg).ExecuteStream
	if transport == "websocket" {
		execute = NewCodexWebsocketsExecutor(cfg).ExecuteStream
	}
	headers := http.Header{"Session-Id": {"client-session"}}
	headers.Set(codexResponsesLiteHeader, "true")
	return execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":[],"prompt_cache_key":"client-session"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatCodex,
		Stream:         true,
		Headers:        headers,
	})
}
