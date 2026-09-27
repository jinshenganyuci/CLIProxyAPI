package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type claudeUpgradeLocalTransport struct {
	base      *url.URL
	transport *http.Transport
}

func (tr claudeUpgradeLocalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	local := req.Clone(req.Context())
	local.URL.Scheme = tr.base.Scheme
	local.URL.Host = tr.base.Host
	return tr.transport.RoundTrip(local)
}

func TestClaudeUpgradeCapacityAndQuotaScopes(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "count", "fast-execute", "fast-stream", "execute-failover", "stream-failover", "fast-decode-execute", "fast-decode-stream"} {
		for _, scenario := range []struct {
			name              string
			shared            bool
			modelLevelCooling bool
		}{
			{name: "overage-with-healthy-subscription"},
			{name: "shared-window-rejected", shared: true},
			{name: "shared-window-model-cooling", shared: true, modelLevelCooling: true},
		} {
			t.Run(mode+"/"+scenario.name, func(t *testing.T) {
				ctx := context.Background()
				decodeFailure := mode == "fast-decode-execute" || mode == "fast-decode-stream"
				const failedModel = "claude-fable-5-1"
				const siblingModel = "claude-opus-5"
				const refusalBody = `{"type":"error","error":{"type":"rate_limit_error","message":"synthetic quota rejection"}}`
				var failedAttempts, siblingAttempts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Model string `json:"model"`
					}
					if errDecode := json.NewDecoder(r.Body).Decode(&body); errDecode != nil {
						http.Error(w, "invalid synthetic request", http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if body.Model == siblingModel {
						siblingAttempts.Add(1)
						_, _ = w.Write([]byte(`{"id":"msg-upgrade-ok","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
						return
					}
					failedAttempts.Add(1)
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
					w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed_warning")
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.00")
					w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
					if scenario.shared {
						w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
					}
					if scenario.shared {
						w.Header().Set("Retry-After", "120")
					} else {
						w.Header().Set("Retry-After", "97273")
					}
					w.Header().Set("X-Synthetic-Upgrade", "preserved")
					if decodeFailure {
						w.Header().Set("Content-Encoding", "gzip")
					}
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(refusalBody))
				}))
				defer server.Close()
				localURL, errURL := url.Parse(server.URL)
				if errURL != nil {
					t.Fatal(errURL)
				}
				transport := &http.Transport{Proxy: nil, DisableCompression: true}
				defer transport.CloseIdleConnections()
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(claudeUpgradeLocalTransport{base: localURL, transport: transport}))
				manager := cliproxyauth.NewManager(nil, nil, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{Claude: config.ClaudeConfig{ModelLevelCooling: scenario.modelLevelCooling}}))
				auth := &cliproxyauth.Auth{ID: "claude-upgrade-" + uuid.NewString(), Provider: "claude", Attributes: map[string]string{"api_key": "synthetic-upgrade-key", "priority": "10"}}
				registered, errRegister := manager.Register(ctx, auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: failedModel}, {ID: siblingModel}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				var releases []func()
				for range 10 {
					release, errAcquire := manager.AcquireCredentialConcurrency(ctx, registered)
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					releases = append(releases, release)
					t.Cleanup(release)
				}
				fast := mode == "fast-execute" || mode == "fast-stream" || decodeFailure
				payload := []byte(`{"model":"claude-fable-5-1","max_tokens":16,"messages":[{"role":"user","content":"synthetic upgrade"}]}`)
				if fast {
					payload = []byte(`{"model":"claude-fable-5-1","max_tokens":16,"speed":"fast","messages":[{"role":"user","content":"synthetic upgrade"}]}`)
				}
				request := cliproxyexecutor.Request{Model: failedModel, Payload: payload}
				options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				run := func() error {
					switch mode {
					case "count":
						_, errRun := manager.ExecuteCount(ctx, []string{"claude"}, request, options)
						return errRun
					case "stream", "fast-stream", "stream-failover", "fast-decode-stream":
						result, errRun := manager.ExecuteStream(ctx, []string{"claude"}, request, options)
						if errRun == nil && result != nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									errRun = chunk.Err
								}
							}
						}
						return errRun
					default:
						_, errRun := manager.Execute(ctx, []string{"claude"}, request, options)
						return errRun
					}
				}
				if errBusy := run(); !cliproxyauth.IsCredentialConcurrencyError(errBusy) {
					t.Fatalf("full credential error = %v, want local capacity 429", errBusy)
				}
				if inflight, limit := manager.CredentialConcurrencySnapshot(auth.ID); inflight != 10 || limit != 10 {
					t.Fatalf("occupied capacity = %d/%d, want 10/10", inflight, limit)
				}
				state, _ := manager.GetByID(auth.ID)
				if failedAttempts.Load() != 0 || state.LastError != nil || state.Unavailable || state.Quota.Exceeded || len(state.ModelStates) != 0 {
					t.Fatalf("local capacity refusal contacted upstream or wrote cooldown: attempts=%d state=%+v", failedAttempts.Load(), state)
				}
				for _, release := range releases {
					release()
				}
				// A lower-priority credential catches hidden rotation inside a single attempt.
				failover := mode == "execute-failover" || mode == "stream-failover"
				if fast || failover {
					alternate := &cliproxyauth.Auth{ID: "claude-upgrade-alternate-" + uuid.NewString(), Provider: "claude", Attributes: map[string]string{"api_key": "synthetic-alternate-key", "priority": "0"}}
					if _, errAlternate := manager.Register(ctx, alternate); errAlternate != nil {
						t.Fatal(errAlternate)
					}
					registry.GetGlobalRegistry().RegisterClient(alternate.ID, "claude", []*registry.ModelInfo{{ID: failedModel}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(alternate.ID) })
				}
				errQuota := run()
				var status interface{ StatusCode() int }
				if errQuota == nil || !errors.As(errQuota, &status) || status.StatusCode() != http.StatusTooManyRequests || cliproxyauth.IsCredentialConcurrencyError(errQuota) {
					t.Fatalf("upstream quota error = %v, want non-capacity 429", errQuota)
				}
				var retry interface{ RetryAfter() *time.Duration }
				if scenario.shared {
					if !errors.As(errQuota, &retry) || retry.RetryAfter() == nil || *retry.RetryAfter() < 120*time.Second || *retry.RetryAfter() > 150*time.Second {
						t.Fatalf("shared quota lost its explicit retry duration: %v", errQuota)
					}
				} else if errors.As(errQuota, &retry) && retry.RetryAfter() != nil {
					t.Fatalf("overage-only quota retained the spend-cap Retry-After: %v", *retry.RetryAfter())
				}
				wantAttempts := int32(1)
				if failover {
					wantAttempts = 2
				}
				if failedAttempts.Load() != wantAttempts {
					t.Fatalf("upstream attempts = %d, want %d", failedAttempts.Load(), wantAttempts)
				}
				if inflight, limit := manager.CredentialConcurrencySnapshot(auth.ID); inflight != 0 || limit != 10 {
					t.Fatalf("released capacity = %d/%d, want 0/10", inflight, limit)
				}
				if fast && !decodeFailure {
					var direct *cliproxyexecutor.RequestTerminatedError
					if !errors.As(errQuota, &direct) || string(direct.Body) != refusalBody || direct.Header.Get("X-Synthetic-Upgrade") != "preserved" {
						t.Fatalf("fast passthrough lost original response: %v", errQuota)
					}
					if !scenario.shared && direct.Header.Get("Retry-After") != "97273" {
						t.Fatal("ignoring overage Retry-After for admission must not rewrite the original response header")
					}
				}
				state, _ = manager.GetByID(auth.ID)
				wantCredential := scenario.shared && !scenario.modelLevelCooling
				if got := state.Quota.Reason == "credential_quota"; got != wantCredential {
					t.Fatalf("credential cooldown = %t, want %t; quota=%+v", got, wantCredential, state.Quota)
				}
				// Fast overage remains request-scoped, preserving the fork's direct-error contract.
				if !fast || scenario.shared || decodeFailure {
					modelState := state.ModelStates[failedModel]
					if modelState == nil || !modelState.Quota.Exceeded || !modelState.Quota.NextRecoverAt.After(time.Now()) {
						t.Fatalf("missing model quota cooldown: %+v", modelState)
					}
					if !scenario.shared && modelState.Quota.NextRecoverAt.After(time.Now().Add(10*time.Minute)) {
						t.Fatalf("overage model cooldown inherited the long spend cap instead of exponential backoff: %+v", modelState.Quota)
					}
				}
				_, errSibling := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: siblingModel, Payload: []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"synthetic sibling"}]}`)}, options)
				if wantCredential {
					if errSibling == nil || siblingAttempts.Load() != 0 {
						t.Fatalf("shared rejection allowed sibling: error=%v attempts=%d", errSibling, siblingAttempts.Load())
					}
				} else if errSibling != nil || siblingAttempts.Load() != 1 {
					t.Fatalf("model rejection blocked sibling: error=%v attempts=%d", errSibling, siblingAttempts.Load())
				}
			})
		}
	}
}
