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
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestVertexInteractionsManagerUsageAndProxyUpgrade823(t *testing.T) {
	for _, mode := range []string{"complete", "truncated", "partial-cancel", "terminal-cancel", "patch-stream", "patch-json", "proxy-failure"} {
		t.Run(mode, func(t *testing.T) {
			var originCalls, proxyCalls atomic.Int32
			stopOrigin := make(chan struct{})
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originCalls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/v1beta1/interactions" || r.Header.Get("Api-Revision") != "synthetic-revision" || r.Header.Get("X-Goog-Api-Key") != "synthetic" {
					t.Error("native Interactions path or credential headers changed")
				}
				if gjson.GetBytes(body, "generation_config.temperature").Float() != 0.4 || gjson.GetBytes(body, "store").Exists() {
					t.Errorf("final payload override/filter was overwritten: %s", body)
				}
				if mode == "patch-json" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"r","steps":[{"type":"function_call","id":"c","name":"apply_patch","arguments":"{\"input\":7}"}],"usage":{"total_input_tokens":11,"total_output_tokens":22,"total_tokens":33}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				payload := `{"event_type":"interaction.complete","interaction":{"id":"r","status":"completed","outputs":[{"type":"text","text":"observed"}],"usage":{"total_input_tokens":11,"total_output_tokens":22,"total_tokens":33}}}`
				if mode == "partial-cancel" || mode == "truncated" {
					payload = `{"event_type":"step.delta","delta":{"type":"text","text":"observed"},"usage":{"total_input_tokens":11,"total_output_tokens":22,"total_tokens":33}}`
				}
				frame := "data: " + payload + "\n\n"
				if mode == "patch-stream" {
					frame = "data: " + `{"event_type":"interaction.created","interaction":{"id":"r"},"usage":{"total_input_tokens":11,"total_output_tokens":22,"total_tokens":33}}` + "\n\n" + task6ProviderFixture("gemini-interactions", "invalid", "apply_patch")
				}
				if mode == "truncated" {
					w.Header().Set("Content-Length", fmt.Sprint(len(frame)+1024))
				}
				_, _ = io.WriteString(w, frame)
				w.(http.Flusher).Flush()
				if strings.HasSuffix(mode, "cancel") {
					select {
					case <-r.Context().Done():
					case <-stopOrigin:
					}
				}
			}))
			defer func() { close(stopOrigin); origin.Close() }()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyCalls.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer proxy.Close()
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: proxy.URL}, Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-3.8-flash", Protocol: "interactions"}}, Params: map[string]any{"generation_config.temperature": 0.4}}},
				Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gemini-3.8-flash", Protocol: "interactions"}}, Params: []string{"store"}}},
			}}
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewGeminiVertexExecutor(cfg))
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "vertex", Status: cliproxyauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": origin.URL, "interactions": "true"}, Metadata: map[string]any{"concurrency_limit": 1}}
			if mode == "proxy-failure" {
				auth.ProxyURL = proxy.URL
			}
			registered, errRegister := manager.Register(context.Background(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "vertex", []*registry.ModelInfo{{ID: "gemini-3.8-flash"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := cliproxyexecutor.Request{Model: "gemini-3.8-flash", Payload: []byte(`{"model":"gemini-3.8-flash","input":"hello","store":true,"tools":[{"type":"custom","name":"apply_patch"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions, ResponseFormat: sdktranslator.FormatInteractions, Headers: http.Header{"Api-Revision": []string{"synthetic-revision"}}}
			var runErr error
			if strings.HasPrefix(mode, "patch") {
				opts.ResponseFormat = sdktranslator.FormatOpenAIResponse
			}
			if mode == "patch-json" {
				_, runErr = manager.Execute(ctx, []string{"vertex"}, req, opts)
			} else {
				opts.Stream = true
				result, errStart := manager.ExecuteStream(ctx, []string{"vertex"}, req, opts)
				runErr = errStart
				if errStart == nil {
					if strings.HasSuffix(mode, "cancel") {
						if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 1 || limit != 1 {
							t.Fatalf("active capacity=%d/%d", active, limit)
						}
					}
					deadline := time.NewTimer(5 * time.Second)
					defer deadline.Stop()
				stream:
					for {
						select {
						case chunk, ok := <-result.Chunks:
							if !ok {
								break stream
							}
							if chunk.Err != nil {
								runErr = chunk.Err
							}
							if strings.HasSuffix(mode, "cancel") && strings.Contains(string(chunk.Payload), "observed") {
								cancel()
							}
						case <-deadline.C:
							t.Fatal("Interactions stream did not settle")
						}
					}
				}
			}
			if (strings.HasPrefix(mode, "patch") || mode == "truncated" || mode == "proxy-failure") && runErr == nil {
				t.Fatal("failed response did not report an error")
			}
			if (mode == "complete" || mode == "terminal-cancel") && runErr != nil {
				t.Fatalf("completed stream failed: %v", runErr)
			}
			if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 0 || limit != 1 {
				t.Fatalf("settled capacity=%d/%d", active, limit)
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), registered)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			release()
			wantOrigin, wantProxy := int32(1), int32(0)
			if mode == "proxy-failure" {
				wantOrigin, wantProxy = 0, 1
			}
			if originCalls.Load() != wantOrigin || proxyCalls.Load() != wantProxy {
				t.Fatalf("unexpected route origin=%d proxy=%d", originCalls.Load(), proxyCalls.Load())
			}
			records := antigravityUsageRecordsAfterDelivery815(t, capture)
			wantFailed := mode != "complete" && mode != "terminal-cancel"
			if len(records) != 1 || records[0].Failed != wantFailed {
				t.Fatalf("usage records=%+v, want one Failed=%t", records, wantFailed)
			}
			if mode != "proxy-failure" && (records[0].Detail.InputTokens != 11 || records[0].Detail.OutputTokens != 22 || records[0].Detail.TotalTokens != 33) {
				t.Fatalf("observed usage lost: %+v", records[0].Detail)
			}
		})
	}
}
