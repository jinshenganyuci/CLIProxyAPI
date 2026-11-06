package executor

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestV858HostedImageFinalPayloadKeepsCredentialIdentityAndUsage(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"forced-rejected", "image-base-does-not-rewrite", "explicit-supported-model", "filtered-force", "auto-choice", "override-to-lite"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", transport, stream, mode), func(t *testing.T) {
					var upstreamCalls atomic.Int32
					captured := make(chan codexHostedImageCapturedRequest, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						upstreamCalls.Add(1)
						var body []byte
						var conn *websocket.Conn
						var errRead error
						if transport == "websocket" {
							conn, errRead = (&websocket.Upgrader{}).Upgrade(w, r, http.Header{"Session-Id": {codexSessionHeaderValue(r.Header)}})
							if errRead != nil {
								t.Error(errRead)
								return
							}
							defer func() { _ = conn.Close() }()
							_, body, errRead = conn.ReadMessage()
						} else {
							body, errRead = io.ReadAll(r.Body)
						}
						if errRead != nil {
							t.Error(errRead)
							return
						}
						captured <- codexHostedImageCapturedRequest{body: body, headers: r.Header.Clone()}
						completed := ojson.NewDocString(`{"type":"response.completed","response":{"id":"v858-image","object":"response","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}}`)
						_ = completed.Set("response.model", ojson.GetBytes(body, "model").String())
						_ = completed.Set("response.metadata.prompt_cache_key", ojson.GetBytes(body, "prompt_cache_key").String())
						_ = completed.Set("response.metadata.x-codex-installation-id", ojson.GetBytes(body, "client_metadata.x-codex-installation-id").String())
						if conn != nil {
							if errWrite := conn.WriteMessage(websocket.TextMessage, completed.Bytes()); errWrite != nil {
								t.Error(errWrite)
							}
						} else {
							w.Header().Set("Content-Type", "text/event-stream")
							w.Header().Set("Session-Id", codexSessionHeaderValue(r.Header))
							_, _ = fmt.Fprintf(w, "data: %s\n\n", completed.Bytes())
						}
					}))
					defer server.Close()

					cfg := codexCredentialIdentityTestConfig(false)
					requestedModel, finalModel := "gpt-6-sol", "gpt-6-sol"
					reject := mode == "forced-rejected" || mode == "image-base-does-not-rewrite" || mode == "override-to-lite"
					switch mode {
					case "image-base-does-not-rewrite":
						cfg.GPTImage2BaseModel = "gpt-5.4-mini"
					case "explicit-supported-model":
						finalModel = "gpt-5.4"
						cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"model": finalModel}}}
					case "filtered-force":
						cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"tools", "tool_choice"}}}
					case "auto-choice":
						cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"tool_choice": "auto"}}}
					case "override-to-lite":
						requestedModel = "gpt-5.4"
						cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"model": finalModel}}}
					}
					auth := codexCredentialIdentityTestAuth(t.Name(), "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
					auth.Attributes = map[string]string{"base_url": server.URL, "plan_type": "pro"}
					auth.ProxyURL = "direct"
					capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
					usage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
					doc := ojson.NewDocString(`{"input":"synthetic hosted image","prompt_cache_key":"session-858","client_metadata":{"x-codex-installation-id":"install-858"},"tools":[{"type":"image_generation"}],"tool_choice":{"type":"image_generation"}}`)
					_ = doc.Set("model", requestedModel)
					req := cliproxyexecutor.Request{Model: requestedModel, Payload: doc.Bytes()}
					originalPayload := bytes.Clone(req.Payload)
					finalized := 0
					opts := cliproxyexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse,
						Headers:  http.Header{"Session-Id": {"session-858"}},
						Metadata: map[string]any{cliproxyexecutor.FinalizedResponseFormatCallbackMetadataKey: func(string) { finalized++ }},
					}
					var exec cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					if transport == "websocket" {
						exec = NewCodexWebsocketsExecutor(cfg)
					}
					var downstream bytes.Buffer
					var executeErr error
					if stream {
						var result *cliproxyexecutor.StreamResult
						result, executeErr = exec.ExecuteStream(t.Context(), auth, req, opts)
						if result != nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									executeErr = chunk.Err
								}
								downstream.Write(chunk.Payload)
							}
						}
					} else {
						var result cliproxyexecutor.Response
						result, executeErr = exec.Execute(t.Context(), auth, req, opts)
						downstream.Write(result.Payload)
					}
					if finalized != 1 || !bytes.Equal(req.Payload, originalPayload) {
						t.Fatalf("payload finalizations=%d or original request mutated", finalized)
					}
					if reject {
						assertCodexHostedImageRequestError(t, executeErr)
						if upstreamCalls.Load() != 0 || downstream.Len() != 0 {
							t.Fatal("unsupported hosted request reached upstream or produced output")
						}
					} else {
						if executeErr != nil {
							t.Fatal(executeErr)
						}
						got := <-captured
						mapped := ojson.GetBytes(got.body, "prompt_cache_key").String()
						if mapped == "" || mapped == "session-858" || mapped != codexSessionHeaderValue(got.headers) {
							t.Fatalf("credential body/header identity mismatch: body=%s", got.body)
						}
						if ojson.GetBytes(got.body, "model").String() != finalModel || got.headers.Get("X-Codex-Routing-Hint") != "model="+finalModel {
							t.Fatalf("final model or routing hint changed: body=%s headers=%v", got.body, got.headers)
						}
						if !bytes.Contains(downstream.Bytes(), []byte(`"prompt_cache_key":"session-858"`)) || !bytes.Contains(downstream.Bytes(), []byte(`"x-codex-installation-id":"install-858"`)) {
							t.Fatalf("downstream identities not restored: %s", downstream.Bytes())
						}
					}
					records := antigravityUsageRecordsAfterDelivery815(t, capture)
					if len(records) != 1 || records[0].Model != requestedModel || records[0].Failed != reject {
						t.Fatalf("usage identity or settlement changed: %+v", records)
					}
					if reject {
						if records[0].Fail.StatusCode != 400 || records[0].Fail.Code != usage.FailureRequestScopedCode || records[0].Detail.TotalTokens != 0 {
							t.Fatalf("local rejection lost request scope or charged tokens: %+v", records[0])
						}
					} else if records[0].ResponseModel != finalModel || records[0].Detail.TotalTokens != 10 {
						t.Fatalf("final response model or usage missing: %+v", records[0])
					}
				})
			}
		}
	}
}

func TestV858StandaloneImagesRemainAvailableWithLiteHintsAndIdentity(t *testing.T) {
	for _, path := range []string{codexImagesGenerationsPath, codexImagesEditsPath} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", path, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					wantPath := codexDirectImagesGenerations
					if path == codexImagesEditsPath {
						wantPath = codexDirectImagesEdit
					}
					if r.URL.Path != wantPath || ojson.GetBytes(body, "model").String() != "gpt-image-2" {
						t.Errorf("independent Images API rerouted: path=%s body=%s", r.URL.Path, body)
					}
					mapped := ojson.GetBytes(body, "client_metadata.x-codex-installation-id").String()
					if mapped == "" || mapped == "install-images-858" {
						t.Errorf("independent Images API lost credential identity: %s", body)
					}
					result := ojson.NewDocString(`{"created":1793952000,"model":"gpt-image-2","data":[{"b64_json":"AA=="}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`)
					_ = result.Set("installation_id", mapped)
					if stream {
						_ = result.Set("type", "image_generation.completed")
						_ = result.Set("b64_json", "AA==")
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: %s\n\n", result.Bytes())
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(result.Bytes())
					}
				}))
				defer server.Close()
				auth := codexCredentialIdentityTestAuth(t.Name(), "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
				auth.ProxyURL = "direct"
				auth.Attributes = map[string]string{"base_url": server.URL}
				cfg := codexCredentialIdentityTestConfig(false)
				cfg.GPTImage2BaseModel = "gpt-6-sol"
				exec := NewCodexExecutor(cfg)
				req := cliproxyexecutor.Request{Model: "gpt-image-2", Payload: []byte(`{"model":"gpt-image-2","prompt":"synthetic image","images":[{"file_id":"file-synthetic"}],"client_metadata":{"x-codex-installation-id":"install-images-858","ws_request_header_x_openai_internal_codex_responses_lite":true}}`)}
				opts := codexOpenAIImageTestOptions(path, stream)
				opts.Headers = http.Header{http.CanonicalHeaderKey(codexResponsesLiteHeader): {"true"}}
				var result []byte
				if stream {
					response, errExecute := exec.ExecuteStream(t.Context(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range response.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						result = append(result, chunk.Payload...)
					}
				} else {
					response, errExecute := exec.Execute(t.Context(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					result = response.Payload
				}
				if !bytes.Contains(result, []byte(`"b64_json":"AA=="`)) || !bytes.Contains(result, []byte(`"installation_id":"install-images-858"`)) {
					t.Fatalf("image result or restored identity missing: %s", result)
				}
			})
		}
	}
}
