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
)

func TestVertexManagerCancellationUsageAndCapacityUpgrade817(t *testing.T) {
	for _, mode := range []string{"completed", "incomplete", "partial", "proxy-failure"} {
		t.Run(mode, func(t *testing.T) {
			var originRequests, proxyRequests atomic.Int32
			originClosed := make(chan struct{})
			stopOrigin := make(chan struct{})
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originRequests.Add(1)
				defer close(originClosed)
				finish := `,"finishReason":"STOP"`
				if mode == "incomplete" {
					finish = `,"finishReason":"MAX_TOKENS"`
				} else if mode == "partial" {
					finish = ""
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"observed\"}]}%s}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":22,\"totalTokenCount\":33}}\n\n", finish)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-stopOrigin:
				}
			}))
			defer func() { close(stopOrigin); origin.Close() }()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyRequests.Add(1)
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, "synthetic proxy failure")
			}))
			defer proxy.Close()

			capture := &providerV810UsageCapture{authID: t.Name(), records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewGeminiVertexExecutor(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: proxy.URL}}))
			auth := &cliproxyauth.Auth{
				ID: t.Name(), Provider: "vertex", Status: cliproxyauth.StatusActive, ProxyURL: "direct",
				Attributes: map[string]string{"api_key": "synthetic", "base_url": origin.URL},
				Metadata:   map[string]any{"concurrency_limit": 1},
			}
			if mode == "proxy-failure" {
				auth.ProxyURL = proxy.URL
			}
			registered, errRegister := manager.Register(context.Background(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "vertex", []*registry.ModelInfo{{ID: "gemini-3.7-flash"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result, errExecute := manager.ExecuteStream(ctx, []string{"vertex"}, cliproxyexecutor.Request{
				Model: "gemini-3.7-flash", Payload: []byte(`{"input":"hello","stream":true}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
			if mode == "proxy-failure" {
				if errExecute == nil || originRequests.Load() != 0 || proxyRequests.Load() != 1 {
					t.Fatalf("proxy failure fell back or retried: err=%v origin=%d proxy=%d", errExecute, originRequests.Load(), proxyRequests.Load())
				}
			} else {
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 1 || limit != 1 {
					t.Fatalf("live stream capacity=%d/%d, want 1/1", active, limit)
				}
				trigger := "response.completed"
				if mode == "incomplete" {
					trigger = "response.incomplete"
				} else if mode == "partial" {
					trigger = "observed"
				}
				canceled := false
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
			stream:
				for {
					select {
					case chunk, ok := <-result.Chunks:
						if !ok {
							break stream
						}
						if chunk.Err != nil && mode != "partial" {
							t.Fatalf("delivered terminal was followed by error: %v", chunk.Err)
						}
						if strings.Contains(string(chunk.Payload), trigger) {
							canceled = true
							cancel()
						}
					case <-deadline.C:
						t.Fatal("stream did not release after cancellation")
					}
				}
				if !canceled || originRequests.Load() != 1 || proxyRequests.Load() != 0 {
					t.Fatalf("credential direct routing/cancel changed: canceled=%t origin=%d proxy=%d", canceled, originRequests.Load(), proxyRequests.Load())
				}
				select {
				case <-originClosed:
				case <-deadline.C:
					t.Fatal("upstream body stayed open after cancellation")
				}
			}
			if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 0 || limit != 1 {
				t.Fatalf("settled capacity=%d/%d, want 0/1", active, limit)
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), registered)
			if errAcquire != nil {
				t.Fatalf("released slot could not be acquired: %v", errAcquire)
			}
			release()
			// The Manager may close its output before the executor publishes deferred usage.
			records := antigravityUsageRecordsAfterDelivery815(t, capture)
			if len(records) != 1 {
				t.Fatalf("usage records=%d, want exactly one", len(records))
			}
			record := records[0]
			wantFailed := mode == "partial" || mode == "proxy-failure"
			if record.Failed != wantFailed {
				t.Fatalf("failed=%t, want %t", record.Failed, wantFailed)
			}
			if mode != "proxy-failure" && (record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 22 || record.Detail.TotalTokens != 33) {
				t.Fatalf("upstream usage lost: %+v", record.Detail)
			}
			if !wantFailed {
				current, _ := manager.GetByID(auth.ID)
				if current.LastError != nil || current.Unavailable || !current.NextRetryAfter.IsZero() {
					t.Fatal("delivered terminal poisoned credential health")
				}
			}
		})
	}
}
