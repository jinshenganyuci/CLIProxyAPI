package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	internalcodex "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Native apply_patch declarations and tool identities must survive the fork's
// namespace transform while all transports retain the selected credential proxy.
func TestV810CodexApplyPatchPreservesCredentialNamespaceAndProxy(t *testing.T) {
	runCodexApplyPatchNamespaceAndProxy(t, false)
}

func TestV815CodexFinalPayloadPreservesCredentialNamespaceAndProxy(t *testing.T) {
	runCodexApplyPatchNamespaceAndProxy(t, true)
}

func runCodexApplyPatchNamespaceAndProxy(t *testing.T, finalOverrides bool) {
	for _, mode := range []string{"http", "compact", "http-stream", "websocket", "websocket-stream"} {
		for _, advertised := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/advertised=%v", mode, advertised), func(t *testing.T) {
				const namespace = "ef49c63c-c044-433d-a85a-0ca341d4f7e9"
				const patch = "*** Begin Patch\n*** Add File: session.txt\n+client-session\n*** End Patch\n"
				const declaration = `{"type":"custom","name":"apply_patch","description":"Keep client-session unchanged.","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}`
				payload := []byte(`{"model":"gpt-5-codex","prompt_cache_key":"client-session","client_metadata":{"x-codex-installation-id":"client-install"},"tools":[` + declaration + `],"input":[{"type":"custom_tool_call","id":"history-item","call_id":"history-call","name":"apply_patch","input":"original patch"},{"type":"custom_tool_call_output","call_id":"history-call","output":"applied"},{"role":"user","content":"continue"}]}`)
				var selectedHits, globalHits, upstreamHits atomic.Int32
				auth := codexCredentialIdentityTestAuth(t.Name(), namespace)
				namespaceID, _, _ := internalcodex.ParseCredentialIdentity(auth.Metadata)
				clientSession, clientInstall := "client-session", "client-install"
				if finalOverrides {
					clientSession, clientInstall = "configured-session", "configured-install"
				}
				wantSession := internalcodex.DeriveCredentialIdentity(namespaceID, "session", clientSession)
				wantInstall := internalcodex.DeriveCredentialIdentity(namespaceID, "installation", clientInstall)
				// Payload overrides do not replace the independently prepared WebSocket
				// handshake identity; both values must remain credential-scoped.
				headerSession := clientSession
				if strings.HasPrefix(mode, "websocket") {
					headerSession = "client-session"
				}
				wantHeaderSession := internalcodex.DeriveCredentialIdentity(namespaceID, "session", headerSession)
				inspect := func(body []byte, headers http.Header) []byte {
					if finalOverrides && (gjson.GetBytes(body, "model").String() != "configured-model" || gjson.GetBytes(body, "instructions").String() != "configured-instructions" || gjson.GetBytes(body, "input.2.content").String() != "configured-message") {
						t.Errorf("final business overrides were lost: %s", body)
					}
					if got := gjson.GetBytes(body, "tools.0").Raw; got != declaration {
						t.Errorf("native grammar changed: %s", got)
					}
					if gjson.GetBytes(body, "input.0.call_id").String() != "history-call" || gjson.GetBytes(body, "input.0.input").String() != "original patch" || gjson.GetBytes(body, "input.1.call_id").String() != "history-call" {
						t.Errorf("native tool history changed: %s", body)
					}
					if gjson.GetBytes(body, "prompt_cache_key").String() != wantSession || codexSessionHeaderValue(headers) != wantHeaderSession || gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String() != wantInstall {
						t.Errorf("native tool request bypassed credential namespace: body=%s session=%q", body, codexSessionHeaderValue(headers))
					}
					return []byte(fmt.Sprintf(`{"id":"response-patch","object":"response","status":"completed","output":[{"type":"custom_tool_call","id":"patch-item","call_id":"patch-call","name":"apply_patch","input":%q}],"metadata":{"prompt_cache_key":%q,"x-codex-installation-id":%q},"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`, patch, wantSession, wantInstall))
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits.Add(1)
					if websocket.IsWebSocketUpgrade(r) {
						conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{"Session-Id": {codexSessionHeaderValue(r.Header)}})
						if errUpgrade != nil {
							t.Error(errUpgrade)
							return
						}
						defer func() { _ = conn.Close() }()
						_, body, errRead := conn.ReadMessage()
						if errRead != nil {
							t.Error(errRead)
							return
						}
						response := inspect(body, r.Header)
						if errWrite := conn.WriteMessage(websocket.TextMessage, append(append([]byte(`{"type":"response.completed","response":`), response...), '}')); errWrite != nil {
							t.Error(errWrite)
						}
						return
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					response := inspect(body, r.Header)
					w.Header().Set("Session-Id", codexSessionHeaderValue(r.Header))
					if mode == "compact" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(response)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
				}))
				defer upstream.Close()
				target, errParse := url.Parse(upstream.URL)
				if errParse != nil {
					t.Fatal(errParse)
				}
				reverse := httputil.NewSingleHostReverseProxy(target)
				reverse.Transport = &http.Transport{Proxy: nil}
				defer reverse.Transport.(*http.Transport).CloseIdleConnections()
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					selectedHits.Add(1)
					if r.Method != http.MethodConnect {
						reverse.ServeHTTP(w, r)
						return
					}
					remote, errDial := net.Dial("tcp", target.Host)
					if errDial != nil {
						t.Error(errDial)
						return
					}
					defer func() { _ = remote.Close() }()
					client, buffered, errHijack := w.(http.Hijacker).Hijack()
					if errHijack != nil {
						t.Error(errHijack)
						return
					}
					defer func() { _ = client.Close() }()
					_, _ = fmt.Fprint(buffered, "HTTP/1.1 200 Connection Established\r\n\r\n")
					_ = buffered.Flush()
					done := make(chan struct{})
					go func() { _, _ = io.Copy(remote, buffered); _ = remote.Close(); close(done) }()
					_, _ = io.Copy(client, remote)
					_ = client.Close()
					<-done
				}))
				defer selected.Close()
				global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { globalHits.Add(1); w.WriteHeader(http.StatusBadGateway) }))
				defer global.Close()
				cfg := codexCredentialIdentityTestConfig(false)
				cfg.Codex.CredentialProxyPolicy = "require"
				cfg.ProxyURL = global.URL
				cfg.Client.Codex.EnableApplyPatch = advertised
				if finalOverrides {
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{
						"model": "configured-model", "instructions": "configured-instructions", "input.2.content": "configured-message",
						"prompt_cache_key": clientSession, "client_metadata.x-codex-installation-id": clientInstall,
					}}}
				}
				auth.ProxyURL = selected.URL
				auth.Attributes = map[string]string{"base_url": upstream.URL}
				req := coreexecutor.Request{Model: "gpt-5-codex", Payload: payload}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}
				httpExec := NewCodexExecutor(cfg)
				wsExec := NewCodexWebsocketsExecutor(cfg)
				wsExec.store = &codexWebsocketSessionStore{}
				var response coreexecutor.Response
				var errExecute error
				if strings.HasSuffix(mode, "stream") {
					var stream *coreexecutor.StreamResult
					if mode == "websocket-stream" {
						stream, errExecute = wsExec.ExecuteStream(t.Context(), auth, req, opts)
					} else {
						stream, errExecute = httpExec.ExecuteStream(t.Context(), auth, req, opts)
					}
					if errExecute == nil {
						response.Headers = stream.Headers
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							for _, event := range applyPatchTestPayloads(chunk.Payload) {
								if gjson.GetBytes(event, "type").String() == "response.completed" {
									response.Payload = []byte(gjson.GetBytes(event, "response").Raw)
								}
							}
						}
					}
				} else if mode == "websocket" {
					response, errExecute = wsExec.Execute(t.Context(), auth, req, opts)
				} else {
					if mode == "compact" {
						opts.Alt = "responses/compact"
					}
					response, errExecute = httpExec.Execute(t.Context(), auth, req, opts)
				}
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				// Non-stream WebSocket execution does not expose handshake headers.
				if mode == "websocket" {
					if len(response.Headers) != 0 {
						t.Fatalf("non-stream WebSocket unexpectedly exposed handshake headers: %v", response.Headers)
					}
				} else if response.Headers.Get("Session-Id") != headerSession {
					t.Fatalf("response Session-Id was not restored: %v", response.Headers)
				}
				if gjson.GetBytes(response.Payload, "metadata.prompt_cache_key").String() != clientSession || gjson.GetBytes(response.Payload, "metadata.x-codex-installation-id").String() != clientInstall {
					t.Fatalf("response identity was not restored: headers=%v payload=%s", response.Headers, response.Payload)
				}
				item := gjson.GetBytes(response.Payload, "output.0")
				if item.Get("type").String() != "custom_tool_call" || item.Get("id").String() != "patch-item" || item.Get("call_id").String() != "patch-call" || item.Get("input").String() != patch {
					t.Fatalf("native patch identity or input changed: %s", response.Payload)
				}
				if bytes.Contains(response.Payload, []byte(wantSession)) || selectedHits.Load() != 1 || upstreamHits.Load() != 1 || globalHits.Load() != 0 {
					t.Fatalf("identity or route leak: selected=%d upstream=%d global=%d payload=%s", selectedHits.Load(), upstreamHits.Load(), globalHits.Load(), response.Payload)
				}
				conflicting := coreexecutor.WithRequestProxyURL(context.Background(), global.URL)
				if _, errConflict := httpExec.Execute(conflicting, auth, req, opts); errConflict == nil {
					t.Fatal("apply_patch request bypassed credential proxy conflict rejection")
				}
				if selectedHits.Load() != 1 || upstreamHits.Load() != 1 || globalHits.Load() != 0 {
					t.Fatal("conflicting request contacted a proxy")
				}
			})
		}
	}
}

type v810CodexPatchTranslationFailure struct{}

func (v810CodexPatchTranslationFailure) ToolInputError() error {
	return fmt.Errorf("invalid upstream apply_patch input: RAW_SECRET")
}

// A completed upstream response is not successful usage when downstream tool
// translation fails. Namespace restoration must happen before that translation.
func TestV810CodexApplyPatchTranslationFailureKeepsFailureAccounting(t *testing.T) {
	for _, compact := range []bool{false, true} {
		for _, retainedError := range []bool{false, true} {
			t.Run(fmt.Sprintf("compact=%v/retained-error=%v", compact, retainedError), func(t *testing.T) {
				cfg := codexCredentialIdentityTestConfig(false)
				auth := codexCredentialIdentityTestAuth(t.Name(), "fdc21986-ed48-4d08-aa6d-e20e701d63d0")
				auth.ProxyURL = "direct"
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					session := gjson.GetBytes(body, "prompt_cache_key").String()
					if session == "client-session" || session == "" {
						t.Errorf("unmapped upstream session: %s", body)
					}
					object := "response"
					if compact {
						object = "response.compaction"
					}
					response := fmt.Sprintf(`{"id":"response-patch-failure","object":%q,"model":"gpt-5-codex-upstream","output":[],"metadata":{"prompt_cache_key":%q},"usage":{"input_tokens":11,"output_tokens":22,"total_tokens":33,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":4}}}`, object, session)
					if compact {
						_, _ = io.WriteString(w, response)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
				}))
				defer upstream.Close()
				auth.Attributes = map[string]string{"base_url": upstream.URL}
				source := sdktranslator.FormatCodex
				if compact {
					source = sdktranslator.FormatOpenAIResponse
				}
				target := sdktranslator.Format("v810-codex-patch-failure-test")
				calls := 0
				sdktranslator.Register(target, source, nil, sdktranslator.ResponseTransform{NonStream: func(_ context.Context, _ string, _, _, body []byte, param *any) []byte {
					calls++
					path := "response.metadata.prompt_cache_key"
					if compact {
						path = "metadata.prompt_cache_key"
					}
					if got := gjson.GetBytes(body, path).String(); got != "client-session" {
						t.Errorf("translator received upstream identity %q: %s", got, body)
					}
					if retainedError {
						*param = v810CodexPatchTranslationFailure{}
						return []byte(`{"input":"RAW_SECRET"}`)
					}
					return nil
				}})
				defer sdktranslator.Unregister(target, source)
				checkUsage := v810CodexPatchFailureUsage(t, auth.ID)
				defer checkUsage()
				payload := []byte(`{"prompt_cache_key":"client-session","input":"patch","tools":[{"type":"custom","name":"apply_patch"}]}`)
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: target, OriginalRequest: payload}
				if compact {
					opts.Alt = "responses/compact"
				}
				response, errExecute := NewCodexExecutor(cfg).Execute(t.Context(), auth, coreexecutor.Request{Model: "gpt-5-codex", Payload: payload}, opts)
				assertTask6PatchError(t, errExecute)
				if calls != 1 || len(response.Payload) != 0 {
					t.Fatalf("unsafe failed translation: calls=%d payload=%s", calls, response.Payload)
				}
			})
		}
	}
}

func v810CodexPatchFailureUsage(t *testing.T, id string) func() {
	t.Helper()
	capture := &task6UsageCapture{id: id, records: make(chan usage.Record, 16)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{}) })
	return func() {
		usage.PublishRecord(context.Background(), usage.Record{RequestID: id + "-barrier"})
		count := 0
		for {
			select {
			case record := <-capture.records:
				if record.RequestID == id+"-barrier" {
					if count != 1 {
						t.Errorf("usage records = %d, want exactly one failure", count)
					}
					return
				}
				count++
				if !record.Failed || record.Detail.TotalTokens != 33 || record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 22 || record.Detail.CachedTokens != 3 || record.Detail.ReasoningTokens != 4 || record.ResponseModel != "gpt-5-codex-upstream" {
					t.Errorf("failed translation lost observed usage: failed=%v input=%d output=%d total=%d cached=%d reasoning=%d response_model=%q", record.Failed, record.Detail.InputTokens, record.Detail.OutputTokens, record.Detail.TotalTokens, record.Detail.CachedTokens, record.Detail.ReasoningTokens, record.ResponseModel)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("usage barrier did not arrive")
			}
		}
	}
}
