package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Exercise the real Manager, executor and uTLS CONNECT path with a credential
// route competing against a global route and an injected transport. Every
// retry must acquire and release the same credential lease and decorate once.
func TestV830ClaudeTLSRetryKeepsCredentialProxyAndLease(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "caller_cancel", "caller_cancel_bound", "caller_deadline", "chrome_cancel"} {
		t.Run(mode, func(t *testing.T) {
			selected, selectedURL := startSilentConnectProxy(t)
			var globalHits, injectedHits, wrappers, attempts atomic.Int32
			global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				globalHits.Add(1)
				http.Error(w, "unexpected global fallback", http.StatusBadGateway)
			}))
			t.Cleanup(global.Close)
			cfg := &config.Config{}
			cfg.ProxyURL = global.URL
			cfg.TLSHandshakeTimeoutSeconds = 1
			if mode == "caller_cancel" || mode == "chrome_cancel" {
				cfg.TLSHandshakeTimeoutSeconds = 0
			}
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(1, 0, 0)
			manager.RegisterExecutor(NewClaudeExecutor(cfg))
			auth := &cliproxyauth.Auth{
				ID: t.Name(), Provider: "claude", ProxyURL: selectedURL,
				Attributes: map[string]string{"api_key": "sk-ant-api03-local-test", "base_url": "https://api.anthropic.com"},
			}
			if mode == "chrome_cancel" {
				auth.Attributes["base_url"] = "https://chatgpt.com"
			}
			registered, errRegister := manager.Register(t.Context(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			const model = "claude-sonnet-4-6"
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "caller_deadline" {
				ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(t.Context())
			}
			defer cancel()
			ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
				injectedHits.Add(1)
				return nil, fmt.Errorf("unexpected injected fallback")
			}))
			ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
				wrappers.Add(1)
				return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					attempts.Add(1)
					if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 1 || limit != 10 {
						t.Errorf("attempt lease = %d/%d, want 1/10", active, limit)
					}
					return next.RoundTrip(req)
				})
			})
			done := make(chan error, 1)
			go func() {
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				if mode == "stream" {
					_, errStream := manager.ExecuteStream(ctx, []string{"claude"}, req, opts)
					done <- errStream
					return
				}
				_, errExecute := manager.Execute(ctx, []string{"claude"}, req, opts)
				done <- errExecute
			}()
			wantAttempts := int32(2)
			callerEnds := mode != "execute" && mode != "stream"
			if callerEnds {
				select {
				case <-selected.started:
					if mode != "caller_deadline" {
						cancel()
					}
				case <-time.After(5 * time.Second):
					t.Fatal("selected proxy did not receive ClientHello")
				}
				wantAttempts = 1
			}
			select {
			case errExecute := <-done:
				if callerEnds {
					wantErr := context.Canceled
					if mode == "caller_deadline" {
						wantErr = context.DeadlineExceeded
					}
					if !errors.Is(errExecute, wantErr) || cliproxyexecutor.IsConfiguredTLSHandshakeTimeoutError(errExecute) {
						t.Fatalf("caller cancellation misclassified: %v", errExecute)
					}
				} else if !cliproxyexecutor.IsConfiguredTLSHandshakeTimeoutError(errExecute) {
					t.Fatalf("expected configured handshake timeout, got %v", errExecute)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("bounded handshake execution did not finish")
			}
			for i := int32(0); i < wantAttempts; i++ {
				select {
				case <-selected.closed:
				case <-time.After(5 * time.Second):
					t.Fatal("selected proxy socket leaked after handshake failure")
				}
			}
			if selected.attempts.Load() != wantAttempts || attempts.Load() != wantAttempts || wrappers.Load() != wantAttempts || globalHits.Load() != 0 || injectedHits.Load() != 0 {
				t.Fatalf("route/decorator counts: selected=%d attempts=%d wrappers=%d global=%d injected=%d; want %d/attempt and no fallback", selected.attempts.Load(), attempts.Load(), wrappers.Load(), globalHits.Load(), injectedHits.Load(), wantAttempts)
			}
			if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 0 || limit != 10 {
				t.Fatalf("settled lease = %d/%d, want 0/10", active, limit)
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), registered)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			release()
		})
	}
}
