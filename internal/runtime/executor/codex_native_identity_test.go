package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	internalcodex "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexNativeCredentialIdentityHeaderShapes(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
	}{
		{name: "none", headers: http.Header{}},
		{name: "hyphen", headers: http.Header{"Session-Id": {"session-a"}}},
		{name: "underscore", headers: http.Header{"session_id": {"session-a"}}},
		{name: "both_equal", headers: http.Header{"Session-Id": {"session-a"}, "session_id": {"session-a"}}},
		{name: "both_different", headers: http.Header{"Session-Id": {"session-a"}, "session_id": {"session-b"}}},
		{name: "multiple_values", headers: http.Header{
			"Session-Id":        {"session-a", "session-b"},
			"session_id":        {"session-c", "session-d"},
			"Conversation_id":   {"conversation-a", "conversation-b"},
			"Thread-Id":         {"thread-a", "thread-b"},
			"X-Codex-Window-Id": {"window-a", "window-b"},
		}},
		{name: "map_key_case", headers: http.Header{"sEsSiOn-Id": {"session-a"}, "tHrEaD-iD": {"thread-a"}}},
	}
	for _, identityEnabled := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("identity=%t/%s", identityEnabled, tc.name), func(t *testing.T) {
				cfg := codexCredentialIdentityTestConfig(false)
				cfg.Codex.CredentialIdentity.Enabled = identityEnabled
				cfg.Codex.DisableCodexCloaking = true
				auth := codexCredentialIdentityTestAuth("native.json", "3cb59d69-5897-49ef-a82b-a4a47115a51e")
				client := tc.headers.Clone()
				client.Set(codexResponsesLiteHeader, "true")
				client.Set("X-Codex-Turn-State", "opaque-turn-state")
				client["X-Codex-Routing-Hint"] = []string{"opaque-route-a", "opaque-route-b"}
				client.Set("X-Codex-Turn-Metadata", `{"future":"opaque","turn_id":"turn-a"}`)
				originalClient := client.Clone()
				body := []byte(`{"prompt_cache_key":"body-session"}`)
				_, state, errIdentity := applyCodexIdentityBody(cfg, auth, body, body)
				if errIdentity != nil {
					t.Fatal(errIdentity)
				}
				headers := applyCodexWebsocketHeaders(context.Background(), http.Header{"session_id": {"generated-session"}, "Conversation_id": {"generated-session"}}, auth, "token", cfg, true, client)
				beforeIdentity := headers.Clone()
				applyCodexIdentityHeaders(headers, &state, true)
				if len(headers) != len(beforeIdentity) {
					t.Fatalf("native header keys changed: before=%v after=%v", beforeIdentity, headers)
				}
				namespace, _, _ := internalcodex.ParseCredentialIdentity(auth.Metadata)
				for key, originalValues := range beforeIdentity {
					values, exists := headers[key]
					if !exists || len(values) != len(originalValues) {
						t.Fatalf("header %s shape changed: before=%v after=%v", key, originalValues, values)
					}
					kind := codexIdentityJSONKind(key)
					for index, original := range originalValues {
						want := original
						if identityEnabled && kind != "" && kind != "turn_metadata" {
							want = internalcodex.DeriveCredentialIdentity(namespace, kind, original)
						}
						if kind == "turn_metadata" && identityEnabled {
							if gjson.Get(values[index], "turn_id").String() != internalcodex.DeriveCredentialIdentity(namespace, "turn", "turn-a") || gjson.Get(values[index], "future").String() != "opaque" {
								t.Fatalf("turn metadata mapping lost: %s", values[index])
							}
						} else if values[index] != want {
							t.Errorf("header %s[%d] = %q, want %q", key, index, values[index], want)
						}
					}
				}
				mapped := headers.Clone()
				if restored := exposeCodexIdentityHeaders(headers, state); !reflect.DeepEqual(restored, beforeIdentity) {
					t.Errorf("response header round trip lost shape/values: got=%v want=%v", restored, beforeIdentity)
				}
				if !reflect.DeepEqual(headers, mapped) || !reflect.DeepEqual(client, originalClient) {
					t.Fatal("response restoration or request preparation mutated its input headers")
				}
			})
		}
	}
}

func TestCodexNativeCredentialIdentityCompatibilityHeadersUnchanged(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, disableCloaking := range []bool{false, true} {
			if native && disableCloaking {
				continue
			}
			t.Run(fmt.Sprintf("native=%t/cloaking_disabled=%t", native, disableCloaking), func(t *testing.T) {
				cfg := codexCredentialIdentityTestConfig(false)
				cfg.Codex.DisableCodexCloaking = disableCloaking
				auth := codexCredentialIdentityTestAuth("compat.json", "3cb59d69-5897-49ef-a82b-a4a47115a51e")
				_, state, errIdentity := applyCodexIdentityBody(cfg, auth, nil, nil)
				if errIdentity != nil {
					t.Fatal(errIdentity)
				}
				headers := applyCodexWebsocketHeaders(context.Background(), nil, auth, "token", cfg, native, http.Header{"Session-Id": {"session-a", "session-b"}})
				applyCodexIdentityHeaders(headers, &state, native && disableCloaking)
				namespace, _, _ := internalcodex.ParseCredentialIdentity(auth.Metadata)
				want := internalcodex.DeriveCredentialIdentity(namespace, "session", "session-a")
				if got := headers["session_id"]; !reflect.DeepEqual(got, []string{want}) {
					t.Errorf("compatibility session alias = %v, want [%s]", got, want)
				}
				if headerValueCaseInsensitive(headers, "Session-Id") != "" || state.preserveNativeHeaders {
					t.Fatal("compatibility mode gained native header behavior")
				}
			})
		}
	}
}

func TestCodexNativeCredentialIdentityWebsocketIntegration(t *testing.T) {
	testCodexNativeCredentialIdentityWebsocketIntegration(t, false)
}

func TestCodexPerCredentialCloakingNativeIdentityWebsocketIntegration(t *testing.T) {
	testCodexNativeCredentialIdentityWebsocketIntegration(t, true)
}

func testCodexNativeCredentialIdentityWebsocketIntegration(t *testing.T, perCredential bool) {
	t.Helper()
	for _, stream := range []bool{false, true} {
		for _, identityEnabled := range []bool{false, true} {
			for _, marker := range []string{"header", "metadata"} {
				for _, buffering := range []bool{false, true} {
					t.Run(fmt.Sprintf("stream=%t/identity=%t/marker=%s/buffer=%t", stream, identityEnabled, marker, buffering), func(t *testing.T) {
						metadata := `{"type":"codex.response.metadata","headers":{"x-codex-turn-state":"opaque-next-state"},"future":{"ok":true}}`
						capturedHeaders := make(chan http.Header, 1)
						capturedBody := make(chan []byte, 1)
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							capturedHeaders <- r.Header.Clone()
							echo := http.Header{"Session-Id": r.Header.Values("Session-Id"), "Session_id": r.Header.Values("Session_id")}
							upgrader := websocket.Upgrader{}
							conn, errUpgrade := upgrader.Upgrade(w, r, echo)
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
							capturedBody <- body
							completed := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_native","status":"completed","output":[],"metadata":{"prompt_cache_key":%q},"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, gjson.GetBytes(body, "prompt_cache_key").String())
							for _, event := range []string{metadata, `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`, completed} {
								if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
									t.Error(errWrite)
									return
								}
							}
						}))
						defer server.Close()
						cfg := codexCredentialIdentityTestConfig(false)
						cfg.Codex.CredentialIdentity.Enabled = identityEnabled
						cfg.Codex.DisableCodexCloaking = true
						cfg.Codex.StreamBootstrapBuffering = buffering
						auth := codexCredentialIdentityTestAuth("native-integration.json", "3cb59d69-5897-49ef-a82b-a4a47115a51e")
						auth.Attributes = map[string]string{"base_url": server.URL}
						if perCredential {
							cfg.Codex.DisableCodexCloaking = false
							auth.Attributes["codex_disable_cloaking"] = "true"
							auth.Attributes["header:User-Agent"] = "FixtureCodexClient/1.0"
							auth.Attributes["header:Originator"] = "fixture-codex"
						}
						auth.ProxyURL = "direct"
						body := []byte(`{"model":"gpt-5.6-sol","input":[],"prompt_cache_key":"body-session"}`)
						headers := http.Header{"Session-Id": {"session-a", "session-b"}, "Session_id": {"session-c", "session-d"}, "Thread-Id": {"thread-a", "thread-b"}, "X-Codex-Turn-State": {"opaque-client-state"}}
						if marker == "header" {
							headers.Set(codexResponsesLiteHeader, "true")
						} else {
							body = []byte(`{"model":"gpt-5.6-sol","input":[],"prompt_cache_key":"body-session","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`)
						}
						req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: body}
						opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatCodex, Stream: stream, Headers: headers}
						executor := NewCodexWebsocketsExecutor(cfg)
						var terminal []byte
						if stream {
							result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
							if errExecute != nil {
								t.Fatal(errExecute)
							}
							for _, header := range []string{"Session-Id", "Session_id"} {
								if !reflect.DeepEqual(result.Headers.Values(header), headers.Values(header)) {
									t.Errorf("downstream %s = %v, want %v", header, result.Headers.Values(header), headers.Values(header))
								}
							}
							var metadataEvents []string
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Fatal(chunk.Err)
								}
								for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
									data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
									switch gjson.GetBytes(data, "type").String() {
									case "codex.response.metadata":
										metadataEvents = append(metadataEvents, string(data))
									case "response.completed":
										terminal = bytes.Clone(data)
									}
								}
							}
							if !reflect.DeepEqual(metadataEvents, []string{metadata}) {
								t.Errorf("opaque native metadata changed: %v", metadataEvents)
							}
							if gjson.GetBytes(terminal, "response.output").Raw != "[]" || gjson.GetBytes(terminal, "response.metadata.prompt_cache_key").String() != "body-session" {
								t.Errorf("native output or identity restoration lost: %s", terminal)
							}
						} else {
							result, errExecute := executor.Execute(context.Background(), auth, req, opts)
							if errExecute != nil {
								t.Fatal(errExecute)
							}
							if gjson.GetBytes(result.Payload, "response.metadata.prompt_cache_key").String() != "body-session" || gjson.GetBytes(result.Payload, "response.output.0.id").String() != "msg_native" {
								t.Errorf("non-stream identity restoration or output assembly lost: %s", result.Payload)
							}
						}
						upstream := <-capturedHeaders
						if perCredential && (upstream.Get("User-Agent") != "FixtureCodexClient/1.0" || upstream.Get("Originator") != "fixture-codex") {
							t.Errorf("per-credential uncloaked headers lost: %v", upstream)
						}
						upstreamBody := <-capturedBody
						namespace, _, _ := internalcodex.ParseCredentialIdentity(auth.Metadata)
						for _, header := range []string{"Session-Id", "Session_id", "Thread-Id"} {
							want := append([]string(nil), headers.Values(header)...)
							if identityEnabled {
								for index, value := range want {
									want[index] = internalcodex.DeriveCredentialIdentity(namespace, "session", value)
								}
							}
							if !reflect.DeepEqual(upstream.Values(header), want) {
								t.Errorf("upstream %s = %v, want %v", header, upstream.Values(header), want)
							}
						}
						if upstream.Get("X-Codex-Turn-State") != "opaque-client-state" || gjson.GetBytes(upstreamBody, "instructions").Exists() {
							t.Errorf("native request fidelity lost: state=%q body=%s", upstream.Get("X-Codex-Turn-State"), upstreamBody)
						}
						if marker == "metadata" && !strings.Contains(string(upstreamBody), "ws_request_header_x_openai_internal_codex_responses_lite") {
							t.Errorf("native metadata marker lost: %s", upstreamBody)
						}
					})
				}
			}
		}
	}
}
