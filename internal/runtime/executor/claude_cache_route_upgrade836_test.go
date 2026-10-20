package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV836ClaudeSearchCacheKeepsCallerCredentialRouteAndLease(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			cache.ClearClaudeWebSearchResultCache()
			t.Cleanup(cache.ClearClaudeWebSearchResultCache)
			var bodies [][]byte
			var mu sync.Mutex
			var selectedHits, fallbackHits, wrappers, attempts atomic.Int32
			var manager *coreauth.Manager
			authIDs := []string{t.Name() + "-a", t.Name() + "-b"}
			handler := claudeWebSearchUpstream(t, &bodies, &mu)
			selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				selectedHits.Add(1)
				id := authIDs[0]
				if r.Header.Get("Authorization") == "Bearer key-b" {
					id = authIDs[1]
				} else if r.Header.Get("Authorization") != "Bearer key-a" {
					t.Errorf("unexpected selected credential header")
				}
				if active, limit := manager.CredentialConcurrencySnapshot(id); active != 1 || limit != 10 {
					t.Errorf("active lease = %d/%d, want 1/10", active, limit)
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil || gjson.GetBytes(body, "operator_marker").String() != "cache836" {
					t.Errorf("final payload missing: body=%s err=%v", body, errRead)
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				handler.ServeHTTP(w, r)
			}))
			defer selected.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackHits.Add(1)
				http.Error(w, "no route fallback", http.StatusBadGateway)
			}))
			defer fallback.Close()
			cfg := enabledClaudeWebSearchCacheConfig()
			cfg.ProxyURL = fallback.URL
			cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"operator_marker": "cache836"}}}
			manager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewClaudeExecutor(cfg))
			const model = "claude-opus-5-5"
			for i, id := range authIDs {
				auth := &coreauth.Auth{ID: id, Provider: "claude", ProxyURL: selected.URL, Attributes: map[string]string{"api_key": []string{"key-a", "key-b"}[i], "base_url": fallback.URL}}
				if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			for _, step := range []struct {
				credential int
				caller     string
				input      string
			}{
				{0, "caller-a", claudeWebSearchExecutorUser},
				{1, "caller-a", claudeWebSearchExecutorHistory},
				{0, "caller-b", claudeWebSearchExecutorHistory},
				{0, "caller-a", claudeWebSearchExecutorHistory},
			} {
				ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ginCtx.Set("userApiKey", step.caller)
				ctx := context.WithValue(t.Context(), "gin", ginCtx)
				ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
					wrappers.Add(1)
					return roundTripperFunc(func(r *http.Request) (*http.Response, error) { attempts.Add(1); return next.RoundTrip(r) })
				})
				payload := []byte(fmt.Sprintf(`{"model":%q,"prompt_cache_key":"same-untrusted-session","stream":%t,"input":[%s]}`, model, stream, step.input))
				req := coreexecutor.Request{Model: model, Payload: payload}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: stream, OriginalRequest: payload, Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: authIDs[step.credential]}}
				if stream {
					result, errStream := manager.ExecuteStream(ctx, []string{"claude"}, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := manager.Execute(ctx, []string{"claude"}, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				if active, limit := manager.CredentialConcurrencySnapshot(authIDs[step.credential]); active != 0 || limit != 10 {
					t.Fatalf("settled lease = %d/%d", active, limit)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 4 {
				t.Fatalf("requests = %d, want 4", len(bodies))
			}
			for _, i := range []int{1, 2} {
				if got := claudeWebSearchResultContent(t, bodies[i]); got != "[]" {
					t.Fatalf("cross credential/caller request %d leaked cached results: %s", i, got)
				}
			}
			if got := claudeWebSearchResultContent(t, bodies[3]); got != claudeWebSearchExecutorResults {
				t.Fatalf("original caller+credential did not restore results: %s", got)
			}
			if selectedHits.Load() != 4 || fallbackHits.Load() != 0 || wrappers.Load() != 4 || attempts.Load() != 4 {
				t.Fatalf("selected/fallback/wrappers/attempts=%d/%d/%d/%d", selectedHits.Load(), fallbackHits.Load(), wrappers.Load(), attempts.Load())
			}
		})
	}
}
