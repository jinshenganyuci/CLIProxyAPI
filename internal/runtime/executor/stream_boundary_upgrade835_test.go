package executor

import (
	"bytes"
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
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Combine the native EOF checks and the new Vertex OpenAPI delegation with the
// fork's real credential route, final payload rules, usage and Manager lease.
func TestV835StreamTerminationKeepsProxyUsageAndLease(t *testing.T) {
	for _, provider := range []string{"claude", "gemini", "vertex", "vertex-openapi"} {
		for _, end := range []string{"truncated", "complete", "cancel", "complete-cancel"} {
			if end == "complete-cancel" && provider != "gemini" && provider != "vertex" {
				continue
			}
			t.Run(provider+"/"+end, func(t *testing.T) {
				var selectedHits, directHits, globalHits, injectedHits, wrappers, attempts atomic.Int32
				var manager *coreauth.Manager
				authID := t.Name()
				upstreamDone := make(chan struct{})
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(upstreamDone)
					selectedHits.Add(1)
					if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 1 || limit != 10 {
						t.Errorf("live lease = %d/%d, want 1/10", active, limit)
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil || gjson.GetBytes(body, "operator_marker").String() != "final835" {
						t.Errorf("final payload rule missing: body=%s err=%v", body, errRead)
					}
					if provider == "vertex-openapi" && (!strings.HasSuffix(r.URL.Path, "/v1/endpoints/openapi/chat/completions") || r.Header.Get("Authorization") != "Bearer local-key") {
						t.Errorf("Vertex partner route changed: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
					}
					w.Header().Set("Content-Type", "text/event-stream")
					switch provider {
					case "claude":
						_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg835\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":12,\"output_tokens\":4}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial835\"}}\n\n")
						if end == "complete" || end == "complete-cancel" {
							_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
						}
					case "gemini", "vertex":
						finish := ""
						if end == "complete" || end == "complete-cancel" {
							finish = `,"finishReason":"STOP"`
						}
						_, _ = fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial835\"}]}%s}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":4,\"totalTokenCount\":16}}\n\n", finish)
					default:
						finish := "null"
						if end == "complete" || end == "complete-cancel" {
							finish = `"stop"`
						}
						_, _ = fmt.Fprintf(w, "data: {\"id\":\"chat835\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial835\"},\"finish_reason\":%s}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":4,\"total_tokens\":16}}\n\n", finish)
					}
					if end == "cancel" || end == "complete-cancel" {
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					}
				}))
				defer selected.Close()
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					directHits.Add(1)
					http.Error(w, "direct route forbidden", http.StatusBadGateway)
				}))
				defer origin.Close()
				global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					globalHits.Add(1)
					http.Error(w, "global route forbidden", http.StatusBadGateway)
				}))
				defer global.Close()
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL}, Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"operator_marker": "final835"}}}}}
				model, authProvider := "gemini-2.5-flash", provider
				var executor coreauth.ProviderExecutor
				switch provider {
				case "claude":
					model, executor = "claude-sonnet-4-6", NewClaudeExecutor(cfg)
				case "gemini":
					executor = NewGeminiExecutor(cfg)
				case "vertex":
					executor = NewGeminiVertexExecutor(cfg)
				default:
					model, authProvider, executor = "deepseek-ai/deepseek-v4.1-flash-maas", "vertex", NewGeminiVertexExecutor(cfg)
				}
				auth := &coreauth.Auth{ID: authID, Provider: authProvider, ProxyURL: selected.URL, Attributes: map[string]string{"api_key": "local-key", "base_url": origin.URL}}
				manager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(executor)
				registered, errRegister := manager.Register(t.Context(), auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(authID, authProvider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { reg.UnregisterClient(authID) })
				capture := &providerV810UsageCapture{authID: authID, records: make(chan usage.Record, 8)}
				usage.RegisterNamedPlugin(authID, capture)
				t.Cleanup(func() { usage.RegisterNamedPlugin(authID, antigravityUsageNoop{}) })
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
					injectedHits.Add(1)
					return nil, fmt.Errorf("injected route forbidden")
				}))
				ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
					wrappers.Add(1)
					return roundTripperFunc(func(r *http.Request) (*http.Response, error) { attempts.Add(1); return next.RoundTrip(r) })
				})
				req := coreexecutor.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`, model))}
				responseFormat, terminal := sdktranslator.FormatClaude, "message_stop"
				if end == "complete-cancel" {
					// Claude emits message_stop at EOF; cancel on an actually delivered
					// Gemini terminal instead of waiting for the upstream to close.
					responseFormat, terminal = sdktranslator.FormatGemini, `"finishReason":"STOP"`
				}
				result, errStream := manager.ExecuteStream(ctx, []string{authProvider}, req, coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: responseFormat, Stream: true})
				if errStream != nil {
					t.Fatal(errStream)
				}
				var received bytes.Buffer
				var streamErr error
				for chunk := range result.Chunks {
					received.Write(chunk.Payload)
					if chunk.Err != nil {
						streamErr = chunk.Err
					}
					if (end == "cancel" && bytes.Contains(chunk.Payload, []byte("partial835"))) ||
						(end == "complete-cancel" && bytes.Contains(chunk.Payload, []byte(terminal))) {
						cancel()
					}
				}
				select {
				case <-upstreamDone:
				case <-time.After(5 * time.Second):
					t.Fatal("upstream did not close after cancellation")
				}
				if end == "truncated" && (streamErr == nil || bytes.Contains(received.Bytes(), []byte("message_stop"))) {
					t.Fatalf("truncation synthesized success or lost error: err=%v body=%s", streamErr, received.Bytes())
				}
				if (end == "complete" || end == "complete-cancel") && (streamErr != nil || !bytes.Contains(received.Bytes(), []byte(terminal))) {
					t.Fatalf("completion lost terminal: err=%v body=%s", streamErr, received.Bytes())
				}
				if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 0 || limit != 10 {
					t.Fatalf("settled lease = %d/%d, want 0/10", active, limit)
				}
				release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), registered)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				release()
				records := antigravityUsageRecordsAfterDelivery815(t, capture)
				if len(records) != 1 || records[0].Failed != (end == "truncated" || end == "cancel") || records[0].Detail.InputTokens != 12 || records[0].Detail.OutputTokens != 4 {
					t.Fatalf("settled usage = %+v, want one %s record with 12/4 tokens", records, end)
				}
				if selectedHits.Load() != 1 || wrappers.Load() != 1 || attempts.Load() != 1 || directHits.Load() != 0 || globalHits.Load() != 0 || injectedHits.Load() != 0 {
					t.Fatalf("route/decorator counts selected/wrappers/attempts/direct/global/injected = %d/%d/%d/%d/%d/%d", selectedHits.Load(), wrappers.Load(), attempts.Load(), directHits.Load(), globalHits.Load(), injectedHits.Load())
				}
			})
		}
	}
}
