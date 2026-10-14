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

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV828CodexSplitSSELifecyclePreservesIdentityUsageAndLease(t *testing.T) {
	for _, mode := range []string{"http", "stream", "cancel"} {
		for _, buffering := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/buffering_%t", mode, buffering), func(t *testing.T) {
				var selectedHits, globalHits, wrappers, attempts atomic.Int32
				var manager *coreauth.Manager
				authID := t.Name()
				upstreamDone := make(chan struct{})
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(upstreamDone)
					selectedHits.Add(1)
					if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 1 || limit != 10 {
						t.Errorf("live credential lease = %d/%d, want 1/10", active, limit)
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					mappedSession := gjson.GetBytes(body, "prompt_cache_key").String()
					if mappedSession == "" || mappedSession == "caller-session" || r.Header.Get("Authorization") == "" {
						t.Errorf("credential request identity missing: session=%q", mappedSession)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					kind, status := "response.completed", "completed"
					if mode == "cancel" {
						kind, status = "response.created", "in_progress"
					}
					event := fmt.Sprintf(`{"type":%q,"response":{"id":"resp_split828","model":"gpt-5.4","status":%q,"output":[],"metadata":{"prompt_cache_key":%q},"usage":{"input_tokens":11,"output_tokens":22,"total_tokens":33}}}`, kind, status, mappedSession)
					// Both identity and usage live in the second physical data line. There is
					// no final newline in the successful case, exercising EOF dispatch.
					cut := strings.Index(event, `"metadata"`)
					_, _ = fmt.Fprintf(w, "event: %s\r\ndata: %s\r\n: keep-alive\r\ndata: %s", kind, event[:cut], event[cut:])
					if mode == "cancel" {
						_, _ = io.WriteString(w, "\r\n\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ready\"}\r\n\r\n")
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					}
				}))
				defer selected.Close()
				global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					globalHits.Add(1)
					http.Error(w, "global route forbidden", http.StatusBadGateway)
				}))
				defer global.Close()
				cfg := codexCredentialIdentityTestConfig(false)
				cfg.ProxyURL = global.URL
				cfg.Codex.StreamBootstrapBuffering = buffering
				auth := codexCredentialIdentityTestAuth(authID, "2b9c5b56-ec3d-4839-994a-5f5c233c34dd")
				auth.ProxyURL = selected.URL
				auth.Attributes = map[string]string{"base_url": "http://codex828.invalid"}
				manager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewCodexExecutor(cfg))
				registered, errRegister := manager.Register(t.Context(), auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
				t.Cleanup(func() { reg.UnregisterClient(authID) })
				capture := &providerV810UsageCapture{authID: authID, records: make(chan usage.Record, 8)}
				usage.RegisterNamedPlugin(authID, capture)
				t.Cleanup(func() { usage.RegisterNamedPlugin(authID, antigravityUsageNoop{}) })
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
					wrappers.Add(1)
					return roundTripperFunc(func(r *http.Request) (*http.Response, error) { attempts.Add(1); return next.RoundTrip(r) })
				})
				request := coreexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":[],"prompt_cache_key":"caller-session"}`)}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: mode != "http"}
				var terminal []byte
				if mode == "http" {
					response, errExecute := manager.Execute(ctx, []string{"codex"}, request, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					terminal = response.Payload
				} else {
					response, errExecute := manager.ExecuteStream(ctx, []string{"codex"}, request, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range response.Chunks {
						if chunk.Err != nil && mode != "cancel" {
							t.Fatal(chunk.Err)
						}
						if mode == "cancel" && bytes.Contains(chunk.Payload, []byte("response.output_text.delta")) {
							cancel()
						}
						if bytes.Contains(chunk.Payload, []byte("response.completed")) {
							terminal = bytes.Clone(chunk.Payload)
						}
					}
				}
				<-upstreamDone
				if mode != "cancel" && (!bytes.Contains(terminal, []byte(`"prompt_cache_key":"caller-session"`)) || !bytes.Contains(terminal, []byte(`"total_tokens":33`))) {
					t.Fatalf("split event lost identity restoration or final usage: %s", terminal)
				}
				if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 0 || limit != 10 {
					t.Fatalf("settled lease = %d/%d", active, limit)
				}
				release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), registered)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				release()
				records := antigravityUsageRecordsAfterDelivery815(t, capture)
				if len(records) != 1 || records[0].Failed != (mode == "cancel") || records[0].Detail.InputTokens != 11 || records[0].Detail.OutputTokens != 22 || records[0].Detail.TotalTokens != 33 {
					t.Fatalf("split event usage must settle once with observed tokens: %+v", records)
				}
				if wrappers.Load() != 1 || attempts.Load() != 1 || selectedHits.Load() != 1 || globalHits.Load() != 0 {
					t.Fatalf("routing/decorator counts = %d/%d/%d/%d", wrappers.Load(), attempts.Load(), selectedHits.Load(), globalHits.Load())
				}
			})
		}
	}
}
