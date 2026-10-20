package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV836ClaudeChatStopOutcomeKeepsUsageRouteAndLease(t *testing.T) {
	for _, stop := range []string{"model_context_window_exceeded", "refusal"} {
		t.Run(stop, func(t *testing.T) {
			var hits, fallbackHits, wrappers, attempts atomic.Int32
			var manager *coreauth.Manager
			authID := t.Name() + "-a"
			selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 1 || limit != 10 {
					t.Errorf("active lease = %d/%d, want 1/10", active, limit)
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil || gjson.GetBytes(body, "operator_marker").String() != "final836" {
					t.Errorf("final payload missing: body=%s err=%v", body, errRead)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg836\",\"model\":\"claude-opus-5-5\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", stop)
			}))
			defer selected.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackHits.Add(1)
				http.Error(w, "no fallback permitted", http.StatusBadGateway)
			}))
			defer fallback.Close()
			const model = "claude-opus-5-5"
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}, Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: model}}, Params: map[string]any{"operator_marker": "final836"}}}}}
			manager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(3, 0, 0)
			manager.RegisterExecutor(NewClaudeExecutor(cfg))
			var first *coreauth.Auth
			for i, id := range []string{authID, t.Name() + "-b"} {
				proxyURL := selected.URL
				if i == 1 {
					proxyURL = fallback.URL
				}
				auth := &coreauth.Auth{ID: id, Provider: "claude", ProxyURL: proxyURL, Attributes: map[string]string{"api_key": "key-local", "base_url": fallback.URL}}
				registered, errRegister := manager.Register(t.Context(), auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				if i == 0 {
					first = registered
				}
				registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			capture := &providerV810UsageCapture{authID: authID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(authID, capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(authID, antigravityUsageNoop{}) })
			ctx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
				wrappers.Add(1)
				return roundTripperFunc(func(r *http.Request) (*http.Response, error) { attempts.Add(1); return next.RoundTrip(r) })
			})
			req := coreexecutor.Request{Model: model, Payload: []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)}
			resp, errExecute := manager.Execute(ctx, []string{"claude"}, req, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
			wantFailure := stop == "model_context_window_exceeded"
			if wantFailure {
				var status interface{ StatusCode() int }
				if !errors.As(errExecute, &status) || status.StatusCode() != http.StatusBadRequest || gjson.Get(errExecute.Error(), "error.code").String() != "context_length_exceeded" {
					t.Fatalf("context failure = %v, want 400/context_length_exceeded", errExecute)
				}
			} else if errExecute != nil || gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String() != "content_filter" {
				t.Fatalf("refusal response = %s, err=%v", resp.Payload, errExecute)
			}
			records := antigravityUsageRecordsAfterDelivery815(t, capture)
			if len(records) != 1 || records[0].Failed != wantFailure || records[0].Detail.InputTokens != 5 || records[0].Detail.OutputTokens != 2 || (wantFailure && records[0].Fail.StatusCode != http.StatusBadRequest) {
				t.Fatalf("usage outcome = %+v, want one failed=%t record with 5/2 tokens", records, wantFailure)
			}
			if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 0 || limit != 10 {
				t.Fatalf("settled lease = %d/%d", active, limit)
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), first)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			release()
			if hits.Load() != 1 || fallbackHits.Load() != 0 || wrappers.Load() != 1 || attempts.Load() != 1 {
				t.Fatalf("hits/fallback/wrappers/attempts = %d/%d/%d/%d", hits.Load(), fallbackHits.Load(), wrappers.Load(), attempts.Load())
			}
		})
	}
}
