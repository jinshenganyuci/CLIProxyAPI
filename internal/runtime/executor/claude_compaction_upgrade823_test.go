package executor

import (
	"context"
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

func TestClaudeCompactionManagerProxyUsageAndCapacityUpgrade823(t *testing.T) {
	for _, mode := range []string{"compact", "stream-trigger", "cancel", "proxy-failure"} {
		t.Run(mode, func(t *testing.T) {
			var originCalls, proxyCalls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originCalls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer synthetic-compaction" {
					t.Error("summary lost selected credential or native path")
				}
				if !strings.Contains(string(body), claudeCompactionSummaryPrompt) || gjson.GetBytes(body, "stream").Bool() {
					t.Error("compaction did not request a nonstream summary")
				}
				if gjson.GetBytes(body, "max_tokens").Int() != 23 || gjson.GetBytes(body, "tool_choice.type").String() != "any" {
					t.Errorf("summary preparation overrode final payload rules: %s", body)
				}
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"msg_compact","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"Sealed summary of the build."}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":7,"cache_creation_input_tokens":3,"cache_read_input_tokens":100}}`)
			}))
			defer origin.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(http.StatusBadGateway) }))
			defer proxy.Close()
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: proxy.URL}, Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "claude-haiku-4-5-20251001", Protocol: "claude"}}, Params: map[string]any{"max_tokens": 23, "tool_choice.type": "any"}}}}}
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewClaudeExecutor(cfg))
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "claude", Status: cliproxyauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic-compaction", "base_url": origin.URL}, Metadata: map[string]any{"concurrency_limit": 1}}
			if mode == "proxy-failure" {
				auth.ProxyURL = proxy.URL
			}
			registered, errRegister := manager.Register(context.Background(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-haiku-4-5-20251001"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := cliproxyexecutor.Request{Model: "claude-haiku-4-5-20251001", Payload: []byte(`{"model":"claude-haiku-4-5-20251001","input":[{"role":"user","content":"summarize the build"},{"type":"compaction_trigger"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact", Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "synthetic-caller"}}
			type outcome struct {
				body []byte
				err  error
			}
			done := make(chan outcome, 1)
			go func() {
				if mode == "stream-trigger" {
					opts.Alt = ""
					opts.Stream = true
					stream, errStart := manager.ExecuteStream(ctx, []string{"claude"}, req, opts)
					if errStart != nil {
						done <- outcome{err: errStart}
						return
					}
					var frames strings.Builder
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							done <- outcome{err: chunk.Err}
							return
						}
						frames.Write(chunk.Payload)
					}
					done <- outcome{body: []byte(frames.String())}
					return
				}
				resp, errExecute := manager.Execute(ctx, []string{"claude"}, req, opts)
				done <- outcome{body: resp.Payload, err: errExecute}
			}()
			if mode != "proxy-failure" {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("summary request did not reach origin")
				}
				if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 1 || limit != 1 {
					t.Fatalf("summary capacity=%d/%d", active, limit)
				}
				if releaseSlot, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), registered); errAcquire == nil {
					releaseSlot()
					t.Fatal("compaction did not occupy credential slot")
				}
				if mode == "cancel" {
					cancel()
				} else {
					close(release)
				}
			}
			var got outcome
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("compaction did not settle")
			}
			failed := mode == "cancel" || mode == "proxy-failure"
			if (got.err != nil) != failed {
				t.Fatalf("compaction err=%v wantFailed=%t", got.err, failed)
			}
			if !failed {
				body := got.body
				if mode == "stream-trigger" {
					body = []byte(claudeCompactionCompletedResponse(t, string(body)).Get("response").Raw)
				}
				if !strings.HasPrefix(gjson.GetBytes(body, "output.0.encrypted_content").String(), "cpa-ag-compact-v1:") || gjson.GetBytes(body, "output.#").Int() != 1 {
					t.Fatalf("missing single native compaction item: %s", body)
				}
				if gjson.GetBytes(body, "usage.total_tokens").Int() != 121 || gjson.GetBytes(body, "usage.input_tokens_details.cached_tokens").Int() != 100 {
					t.Fatalf("compaction usage lost: %s", body)
				}
			}
			if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 0 || limit != 1 {
				t.Fatalf("settled capacity=%d/%d", active, limit)
			}
			releaseSlot, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), registered)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			releaseSlot()
			wantOrigin, wantProxy := int32(1), int32(0)
			if mode == "proxy-failure" {
				wantOrigin, wantProxy = 0, 1
			}
			if originCalls.Load() != wantOrigin || proxyCalls.Load() != wantProxy {
				t.Fatalf("unexpected route/retry origin=%d proxy=%d", originCalls.Load(), proxyCalls.Load())
			}
			records := antigravityUsageRecordsAfterDelivery815(t, capture)
			if len(records) != 1 || records[0].Failed != failed {
				t.Fatalf("summary did not publish one attempt: %+v", records)
			}
			if !failed && (records[0].Detail.InputTokens != 11 || records[0].Detail.OutputTokens != 7 || records[0].Detail.CacheReadTokens != 100 || records[0].Detail.CacheCreationTokens != 3) {
				t.Fatalf("summary accounting lost detail: %+v", records[0].Detail)
			}
		})
	}
}
