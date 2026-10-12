package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestClaudeToolSearchManagerProxyReloadAndUsageUpgrade824(t *testing.T) {
	for _, mode := range []string{"native", "gateway-fallback", "legacy-fallback", "switch-off", "proxy-failure"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", mode, stream), func(t *testing.T) {
				startEnabled := mode != "switch-off"
				enableClaudeToolSearchForTest(t, startEnabled)
				wantNative := mode == "native"
				var originCalls, selectedProxyCalls, globalProxyCalls atomic.Int32
				model := "claude-sonnet-4-5"
				if mode == "legacy-fallback" {
					model = "claude-opus-4-1-20250805"
				}
				var manager *cliproxyauth.Manager
				selectedID := t.Name() + "-native"
				if mode == "gateway-fallback" {
					selectedID = t.Name() + "-gateway"
				}
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					originCalls.Add(1)
					body, _ := io.ReadAll(r.Body)
					if r.Header.Get("Authorization") != "Bearer "+selectedID {
						t.Error("selected credential identity changed")
					}
					if gjson.GetBytes(body, "max_tokens").Int() != 27 {
						t.Error("final payload override did not survive fallback")
					}
					if wantNative {
						assertNativeToolSearchBody(t, body)
					} else {
						assertDevToolSearchBody(t, body)
					}
					if active, limit := manager.CredentialConcurrencySnapshot(selectedID); active != 1 || limit != 10 {
						t.Errorf("active default capacity=%d/%d, want 1/10", active, limit)
					}
					// Flip the live global setting after request translation. The reply
					// must still use the capability pinned for this execution.
					responsesclaude.SetClientToolSearchEnabled(!startEnabled)
					if gjson.GetBytes(body, "stream").Bool() {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+`{"type":"message_start","message":{"id":"msg_same","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":11,"output_tokens":0}}}`+"\n\n"+
							"data: "+`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_search","name":"tool_search","input":{}}}`+"\n\n"+
							"data: "+`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"calendar\"}"}}`+"\n\n"+
							"data: "+`{"type":"content_block_stop","index":0}`+"\n\n"+
							"data: "+`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":22}}`+"\n\n"+
							"data: "+`{"type":"message_stop"}`+"\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"msg_same","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_search","name":"tool_search","input":{"query":"calendar"}}],"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":22}}`)
					}
				}))
				defer origin.Close()
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				defer transport.CloseIdleConnections()
				selectedProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					selectedProxyCalls.Add(1)
					if mode == "proxy-failure" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					forward := r.Clone(r.Context())
					forward.RequestURI = ""
					resp, errDo := transport.RoundTrip(forward)
					if errDo != nil {
						t.Errorf("synthetic proxy: %v", errDo)
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					defer func() { _ = resp.Body.Close() }()
					w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
					w.WriteHeader(resp.StatusCode)
					_, _ = io.Copy(w, resp.Body)
				}))
				defer selectedProxy.Close()
				globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					globalProxyCalls.Add(1)
					w.WriteHeader(http.StatusBadGateway)
				}))
				defer globalProxy.Close()
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: globalProxy.URL}, Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "claude"}}, Params: map[string]any{"max_tokens": 27}}}}}
				manager = cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewClaudeExecutor(cfg))
				for _, kind := range []string{"native", "gateway"} {
					id := t.Name() + "-" + kind
					route := "direct"
					attrs := map[string]string{"api_key": id, "base_url": origin.URL, "cloak_mode": "never"}
					if kind == "native" {
						route = selectedProxy.URL
						attrs["native_tool_search"] = "true"
					}
					auth := &cliproxyauth.Auth{ID: id, Provider: "claude", Status: cliproxyauth.StatusActive, ProxyURL: route, Attributes: attrs}
					if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}
				capture := &providerV810UsageCapture{authID: selectedID, records: make(chan usage.Record, 8)}
				usage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
				payload, _ := sjson.Set(nativeToolSearchResponsesPayload, "stream", stream)
				payload, _ = sjson.Set(payload, "model", model)
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: stream, OriginalRequest: req.Payload, Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: selectedID, cliproxyexecutor.CallerScopeMetadataKey: t.Name()}}
				ctx := usage.WithAccessProvider(context.Background(), "synthetic-access-guard")
				var output []byte
				var runErr error
				if stream {
					result, errStart := manager.ExecuteStream(ctx, []string{"claude"}, req, opts)
					runErr = errStart
					if errStart == nil {
						for chunk := range result.Chunks {
							output = append(output, chunk.Payload...)
							if chunk.Err != nil {
								runErr = chunk.Err
							}
						}
					}
				} else {
					resp, errExecute := manager.Execute(ctx, []string{"claude"}, req, opts)
					runErr = errExecute
					output = resp.Payload
				}
				failed := mode == "proxy-failure"
				if (runErr != nil) != failed {
					t.Fatalf("error=%v want failure=%t", runErr, failed)
				}
				if !failed {
					wantItem := "function_call"
					if wantNative {
						wantItem = "tool_search_call"
					}
					gotType := ""
					if stream {
						for _, line := range strings.Split(string(output), "\n") {
							data := strings.TrimPrefix(line, "data: ")
							if gjson.Get(data, "type").String() == "response.output_item.done" {
								gotType = gjson.Get(data, "item.type").String()
							}
						}
					} else {
						gotType = gjson.GetBytes(output, "output.0.type").String()
					}
					if gotType != wantItem {
						t.Fatalf("reload changed pinned response translation: got %q want %q", gotType, wantItem)
					}
				}
				for _, kind := range []string{"native", "gateway"} {
					if active, limit := manager.CredentialConcurrencySnapshot(t.Name() + "-" + kind); active != 0 || limit != 10 {
						t.Fatalf("settled %s capacity=%d/%d", kind, active, limit)
					}
				}
				wantOrigin, wantProxy := int32(1), int32(1)
				if mode == "gateway-fallback" {
					wantProxy = 0
				}
				if failed {
					wantOrigin = 0
				}
				if originCalls.Load() != wantOrigin || selectedProxyCalls.Load() != wantProxy || globalProxyCalls.Load() != 0 {
					t.Fatalf("route/retry changed: origin=%d selected=%d global=%d", originCalls.Load(), selectedProxyCalls.Load(), globalProxyCalls.Load())
				}
				records := antigravityUsageRecordsAfterDelivery815(t, capture)
				if len(records) != 1 || records[0].Failed != failed || records[0].AccessProvider != "synthetic-access-guard" || records[0].IsNativeKey {
					t.Fatalf("usage classification/once changed: %+v", records)
				}
				if !failed && (records[0].Detail.InputTokens != 11 || records[0].Detail.OutputTokens != 22 || records[0].Detail.TotalTokens != 33) {
					t.Fatalf("usage lost: %+v", records[0].Detail)
				}
			})
		}
	}
}
