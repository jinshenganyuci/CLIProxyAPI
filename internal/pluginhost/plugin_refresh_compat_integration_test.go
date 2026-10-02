package pluginhost

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestPluginRefreshCompatApplyPatchManagerProxyAndAdmission(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, invalid := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/invalid=%t", stream, invalid), func(t *testing.T) {
				const provider = "plugin-patch-integration"
				model := "plugin-patch-integration-model"
				authID := t.Name()
				var requestHooks, beforeHooks, afterHooks, forbiddenRepairs atomic.Int32
				host := newHostWithRecords(capabilityRecord{
					id: "patch-hooks",
					plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						RequestNormalizer: requestNormalizerFunc(func(_ context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
							requestHooks.Add(1)
							body, errSet := sjson.SetBytes(req.Body, "metadata.plugin_marker", "preserved")
							return pluginapi.PayloadResponse{Body: body}, errSet
						}),
						ResponseBeforeTranslator: responseNormalizerFunc(func(_ context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
							beforeHooks.Add(1)
							return pluginapi.PayloadResponse{Body: req.Body}, nil
						}),
						ResponseAfterTranslator: responseNormalizerFunc(func(_ context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
							afterHooks.Add(1)
							if bytes.Contains(req.Body, []byte("invalid_tool_arguments")) {
								forbiddenRepairs.Add(1)
								return pluginapi.PayloadResponse{Body: []byte(`{"type":"response.completed","response":{"status":"completed"}}`)}, nil
							}
							return pluginapi.PayloadResponse{Body: req.Body}, nil
						}),
					}},
				})
				sdktranslator.SetPluginHooks(host)
				t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })

				var proxyCalls, globalCalls atomic.Int32
				global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					globalCalls.Add(1)
					http.Error(w, "unexpected fallback", http.StatusBadGateway)
				}))
				t.Cleanup(global.Close)
				entered := make(chan []byte, 1)
				gate := make(chan struct{})
				var releaseOnce sync.Once
				releaseGate := func() { releaseOnce.Do(func() { close(gate) }) }
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					proxyCalls.Add(1)
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					if r.URL.Host != "plugin-patch.invalid" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-pinned-token" {
						t.Errorf("credential routing changed: method=%s URL=%s", r.Method, r.URL)
					}
					entered <- body
					select {
					case <-gate:
					case <-r.Context().Done():
						return
					}
					arguments := `{"input":"*** Begin Patch\n*** End Patch"}`
					if invalid {
						arguments = `{"input":7,"private":"SYNTHETIC_PRIVATE_PATCH"}`
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: {\"id\":\"resp-plugin\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-plugin\",\"type\":\"function\",\"function\":{\"name\":\"apply_patch\",\"arguments\":%q}}]}}]}\n\n", model, arguments)
						_, _ = io.WriteString(w, "data: {\"id\":\"resp-plugin\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\ndata: [DONE]\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"id":"resp-plugin","object":"chat.completion","model":%q,"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-plugin","type":"function","function":{"name":"apply_patch","arguments":%q}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`, model, arguments)
				}))
				t.Cleanup(proxy.Close)
				t.Cleanup(releaseGate)

				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL, Client: config.ClientConfig{Codex: config.CodexClientConfig{EnableApplyPatch: true}}}}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				wrapped := NewPluginRefreshCompatExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, cfg), host, cfg)
				manager.RegisterExecutor(wrapped)
				auth, errRegister := manager.Register(t.Context(), &coreauth.Auth{
					ID: authID, Provider: provider, Status: coreauth.StatusActive, ProxyURL: proxy.URL,
					Attributes: map[string]string{"api_key": "synthetic-pinned-token", "base_url": "http://plugin-patch.invalid/v1"},
				})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				otherID := authID + "-other"
				if _, errOther := manager.Register(t.Context(), &coreauth.Auth{ID: otherID, Provider: provider, Status: coreauth.StatusActive, ProxyURL: global.URL, Attributes: map[string]string{"api_key": "synthetic-other-token", "base_url": "http://plugin-patch.invalid/v1"}}); errOther != nil {
					t.Fatal(errOther)
				}
				for _, id := range []string{authID, otherID} {
					registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}
				base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
				if !base.SupportsApplyPatchModel(model) || !wrapped.(coreauth.APIKeyConfigExecutor).ForAPIKey().(coreauth.ApplyPatchSupport).SupportsApplyPatch() {
					t.Fatal("plugin wrapper or public alias lost native apply_patch capability")
				}
				for range 9 {
					release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), auth)
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					t.Cleanup(release)
				}
				records := make(chan coreusage.Record, 8)
				registerTestUsagePlugin(t, authID, coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
					if record.AuthID == authID {
						records <- record
					}
				}))
				payload := []byte(fmt.Sprintf(`{"model":%q,"input":"patch","tools":[{"type":"custom","name":"apply_patch"}]}`, model))
				req := coreexecutor.Request{Model: model, Payload: payload}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: authID}}
				type result struct {
					body []byte
					err  error
				}
				done := make(chan result, 1)
				go func() {
					if !stream {
						response, errExecute := manager.Execute(t.Context(), []string{provider}, req, opts)
						done <- result{response.Payload, errExecute}
						return
					}
					response, errStream := manager.ExecuteStream(t.Context(), []string{provider}, req, opts)
					out := result{err: errStream}
					if errStream == nil {
						for chunk := range response.Chunks {
							out.body = append(out.body, chunk.Payload...)
							if chunk.Err != nil {
								if out.err != nil {
									t.Error("duplicate terminal stream error")
								}
								out.err = chunk.Err
							}
						}
					}
					done <- out
				}()
				var upstream []byte
				select {
				case upstream = <-entered:
				case out := <-done:
					t.Fatalf("request did not reach its proxy: %v", out.err)
				case <-time.After(10 * time.Second):
					t.Fatal("timed out waiting for the local proxy")
				}
				if gjson.GetBytes(upstream, "metadata.plugin_marker").String() != "preserved" || gjson.GetBytes(upstream, "tools.0.function.name").String() != "apply_patch" {
					t.Fatalf("plugin hook or apply_patch translation lost: %s", upstream)
				}
				if inflight, limit := manager.CredentialConcurrencySnapshot(authID); inflight != 10 || limit != 10 {
					t.Fatalf("active pinned credential = %d/%d, want 10/10", inflight, limit)
				}
				if _, errOverflow := manager.Execute(t.Context(), []string{provider}, req, opts); !coreauth.IsCredentialConcurrencyError(errOverflow) {
					t.Fatalf("pinned overflow bypassed its own limit: %v", errOverflow)
				}
				releaseGate()
				var out result
				select {
				case out = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("execution did not terminate after releasing the local proxy")
				}
				if invalid {
					status, okStatus := out.err.(interface{ StatusCode() int })
					if !okStatus || status.StatusCode() != http.StatusBadGateway || strings.Contains(string(out.body), "SYNTHETIC_PRIVATE_PATCH") || strings.Contains(string(out.body), "response.completed") {
						t.Fatalf("invalid patch lost its sanitized failure: err=%v body=%s", out.err, out.body)
					}
					if stream && bytes.Count(out.body, []byte(`"type":"response.failed"`)) != 1 {
						t.Fatalf("failed stream must emit exactly one failed response: %s", out.body)
					}
				} else if out.err != nil || !bytes.Contains(out.body, []byte(`"type":"custom_tool_call"`)) || !bytes.Contains(out.body, []byte("*** Begin Patch")) {
					t.Fatalf("successful patch was not restored: err=%v body=%s", out.err, out.body)
				}
				if proxyCalls.Load() != 1 || globalCalls.Load() != 0 || requestHooks.Load() != 1 || beforeHooks.Load() == 0 || forbiddenRepairs.Load() != 0 || (!invalid && afterHooks.Load() == 0) {
					t.Fatalf("proxy/hooks changed: proxy=%d fallback=%d request=%d before=%d after=%d repairs=%d", proxyCalls.Load(), globalCalls.Load(), requestHooks.Load(), beforeHooks.Load(), afterHooks.Load(), forbiddenRepairs.Load())
				}
				if inflight, limit := manager.CredentialConcurrencySnapshot(authID); inflight != 9 || limit != 10 {
					t.Fatalf("terminal path leaked admission: %d/%d", inflight, limit)
				}
				if inflight, limit := manager.CredentialConcurrencySnapshot(otherID); inflight != 0 || limit != 10 {
					t.Fatalf("pinned request spilled to another credential: %d/%d", inflight, limit)
				}
				coreusage.PublishRecord(t.Context(), coreusage.Record{AuthID: authID, Model: "barrier"})
				for i := range 2 {
					select {
					case record := <-records:
						if i == 0 && (record.Model != model || record.Failed != invalid || (!invalid && record.Detail.TotalTokens != 8)) {
							t.Fatalf("terminal accounting changed: %+v", record)
						}
						if i == 1 && record.Model != "barrier" {
							t.Fatalf("duplicate terminal usage: %+v", record)
						}
					case <-time.After(10 * time.Second):
						t.Fatal("usage dispatcher did not reach its barrier")
					}
				}
			})
		}
	}
}
