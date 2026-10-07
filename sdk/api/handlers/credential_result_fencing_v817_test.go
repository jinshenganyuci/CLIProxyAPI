package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type v817ResultHook struct {
	coreauth.NoopHook
	results chan coreauth.Result
}

func (h *v817ResultHook) OnResult(_ context.Context, result coreauth.Result) {
	h.results <- result
}

func awaitV817ResultEvent[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(10 * time.Second):
		t.Fatal("credential result notification did not arrive")
		var zero T
		return zero
	}
}

func TestV817StaleCredentialResultsKeepSDKLifecycleAndAccounting(t *testing.T) {
	for _, replacement := range []string{"token-update", "registration-epoch"} {
		for _, stream := range []bool{false, true} {
			for _, fail := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/fail=%t", replacement, stream, fail), func(t *testing.T) {
					const provider = "v817-result-compat"
					const model = "v817-result-model"
					id := t.Name()
					entered := make(chan struct{}, 1)
					release := make(chan struct{})
					var releaseOnce sync.Once
					unblock := func() { releaseOnce.Do(func() { close(release) }) }
					var requests, fallbackRequests atomic.Int32
					fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						fallbackRequests.Add(1)
						http.Error(w, "unexpected fallback proxy", http.StatusBadGateway)
					}))
					t.Cleanup(fallback.Close)
					proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Error(errRead)
							return
						}
						old := gjson.GetBytes(body, "messages.0.content").String() == "old"
						wantToken := "synthetic-old-token"
						if !old && replacement == "token-update" {
							wantToken = "synthetic-new-token"
						}
						if !r.URL.IsAbs() || r.URL.Host != "v817-upstream.invalid" || r.Header.Get("Authorization") != "Bearer "+wantToken {
							t.Errorf("credential-bound proxy or token snapshot changed: %s", r.URL)
						}
						isStream := gjson.GetBytes(body, "stream").Bool()
						if isStream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: {\"id\":\"old-response\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"old partial\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n")
							w.(http.Flusher).Flush()
						}
						if old {
							entered <- struct{}{}
							select {
							case <-release:
							case <-r.Context().Done():
								return
							}
						}
						if old && fail {
							if isStream {
								_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"status_code\":429,\"type\":\"rate_limit_error\",\"message\":\"old credential rejected\"}}\n\n")
							} else {
								w.Header().Set("Content-Type", "application/json")
								w.WriteHeader(http.StatusTooManyRequests)
								_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"old credential rejected"}}`)
							}
							return
						}
						if isStream {
							_, _ = io.WriteString(w, "data: {\"id\":\"old-response\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\ndata: [DONE]\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"response","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
					}))
					t.Cleanup(proxy.Close)
					t.Cleanup(unblock)
					cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}}
					hook := &v817ResultHook{results: make(chan coreauth.Result, 4)}
					manager := coreauth.NewManager(nil, nil, hook)
					manager.SetConfig(cfg)
					manager.SetRetryConfig(0, 0, 0)
					manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, cfg))
					initial := &coreauth.Auth{ID: id, Provider: provider, Status: coreauth.StatusActive, ProxyURL: proxy.URL, Attributes: map[string]string{"api_key": "synthetic-old-token", "base_url": "http://v817-upstream.invalid/v1"}, Metadata: map[string]any{"codex_identity_namespace": "synthetic-stable-namespace", "opaque_plugin": map[string]any{"enabled": false}}}
					limit := 10
					if replacement == "registration-epoch" {
						limit = 3
						initial.Metadata["concurrency_limit"] = limit
					}
					oldAuth, errRegister := manager.Register(t.Context(), initial)
					if errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					base := NewBaseAPIHandlers(&cfg.SDKConfig, manager)
					completions := make(chan pluginapi.RequestCompletion, 4)
					base.SetPluginHost(&handlerInterceptorTestHost{completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) { completions <- completion }})
					capture := newCapturePluginExecutorUsagePlugin(provider)
					registerUsagePluginForTest(t, id, capture)
					type executionResult struct {
						err  error
						body []byte
						code int
					}
					call := func(marker string, streaming bool) executionResult {
						request := ModelExecutionRequest{Model: model, EntryProtocol: "openai", ExitProtocol: "openai", AuthID: id, Stream: streaming, Body: []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":%q}]}`, model, streaming, marker))}
						if !streaming {
							response, errExecute := base.ExecuteModel(t.Context(), request)
							if errExecute != nil {
								return executionResult{err: errExecute.Error, code: errExecute.StatusCode}
							}
							return executionResult{body: response.Body, code: response.StatusCode}
						}
						response, errExecute := base.ExecuteModelStream(t.Context(), request)
						if errExecute != nil {
							return executionResult{err: errExecute.Error, code: errExecute.StatusCode}
						}
						out := executionResult{code: response.StatusCode}
						for chunk := range response.Chunks {
							out.body = append(out.body, chunk.Payload...)
							if chunk.Err != nil {
								out.err, out.code = chunk.Err, chunk.Err.StatusCode
							}
						}
						return out
					}
					done := make(chan executionResult, 1)
					go func() { done <- call("old", stream) }()
					select {
					case <-entered:
					case <-time.After(10 * time.Second):
						t.Fatal("old credential request did not reach its proxy")
					}
					checkAdmission := func(want int) {
						t.Helper()
						if active, maximum := manager.CredentialConcurrencySnapshot(id); active != want || maximum != limit {
							t.Fatalf("credential admission changed across replacement: %d/%d, want %d/%d", active, maximum, want, limit)
						}
					}
					checkAdmission(1)
					replacementAuth := oldAuth.Clone()
					var currentAuth *coreauth.Auth
					var errUpdate error
					if replacement == "token-update" {
						replacementAuth.Attributes["api_key"] = "synthetic-new-token"
						currentAuth, errUpdate = manager.Update(t.Context(), replacementAuth)
					} else {
						manager.Remove(t.Context(), id)
						currentAuth, errUpdate = manager.Register(t.Context(), replacementAuth)
					}
					if errUpdate != nil {
						t.Fatal(errUpdate)
					}
					if replacement == "token-update" && (currentAuth.CredentialVersion <= oldAuth.CredentialVersion || currentAuth.RegistrationEpoch != oldAuth.RegistrationEpoch) {
						t.Fatal("token replacement did not advance only credential version")
					}
					if replacement == "registration-epoch" && (currentAuth.RegistrationEpoch <= oldAuth.RegistrationEpoch || currentAuth.CredentialVersion != oldAuth.CredentialVersion) {
						t.Fatal("same-secret re-registration did not isolate the epoch fence")
					}
					checkAdmission(1)
					if probe := call("probe", false); probe.err != nil || probe.code != http.StatusOK {
						t.Fatalf("replacement credential is not usable while old execution is active: %+v", probe)
					}
					probeCompletion := awaitV817ResultEvent(t, completions)
					if probeCompletion.Outcome != pluginapi.RequestCompletionSucceeded {
						t.Fatalf("fresh credential plugin lifecycle failed: %+v", probeCompletion)
					}
					probeUsage := capture.waitRecord(t)
					if probeUsage.Failed || probeUsage.Detail.TotalTokens != 8 {
						t.Fatalf("fresh accounting changed: %+v", probeUsage)
					}
					probeResult := awaitV817ResultEvent(t, hook.results)
					if probeResult.CredentialVersion != currentAuth.CredentialVersion || probeResult.RegistrationEpoch != currentAuth.RegistrationEpoch {
						t.Fatalf("fresh execution result lost credential version: %+v", probeResult)
					}
					beforeLate, _ := manager.GetByID(id)
					checkAdmission(1)
					unblock()
					var oldResult executionResult
					select {
					case oldResult = <-done:
					case <-time.After(10 * time.Second):
						t.Fatal("old execution did not finish")
					}
					if (oldResult.err != nil) != fail || (fail && (oldResult.code != http.StatusTooManyRequests || !strings.Contains(oldResult.err.Error(), "old credential rejected"))) {
						t.Fatalf("credential replacement rewrote original request outcome: %+v", oldResult)
					}
					oldCompletion := awaitV817ResultEvent(t, completions)
					wantOutcome := pluginapi.RequestCompletionSucceeded
					if fail {
						wantOutcome = pluginapi.RequestCompletionFailed
					}
					if oldCompletion.Outcome != wantOutcome || oldCompletion.RequestID == probeCompletion.RequestID {
						t.Fatalf("late request lost its independent plugin lifecycle: %+v", oldCompletion)
					}
					oldUsage := capture.waitRecord(t)
					if oldUsage.AuthID != id || oldUsage.Failed != fail || ((!fail || stream) && oldUsage.Detail.TotalTokens != 8) {
						t.Fatalf("stale-result fence swallowed actual accounting: %+v", oldUsage)
					}
					result := awaitV817ResultEvent(t, hook.results)
					if result.CredentialVersion != oldAuth.CredentialVersion || result.RegistrationEpoch != oldAuth.RegistrationEpoch || result.Success == fail {
						t.Fatalf("late hook was not attributed to the original credential snapshot: %+v", result)
					}
					afterLate, _ := manager.GetByID(id)
					if !reflect.DeepEqual(beforeLate, afterLate) {
						t.Fatalf("late result mutated the current credential: before=%+v after=%+v", beforeLate, afterLate)
					}
					checkAdmission(0)
					if requests.Load() != 2 || fallbackRequests.Load() != 0 {
						t.Fatalf("replacement retried stale work or bypassed its proxy: upstream=%d fallback=%d", requests.Load(), fallbackRequests.Load())
					}
					usage.PublishRecord(t.Context(), usage.Record{Provider: provider, Model: "barrier"})
					if next := capture.waitRecord(t); next.Model != "barrier" {
						t.Fatalf("duplicate request accounting: %+v", next)
					}
					select {
					case duplicate := <-completions:
						t.Fatalf("duplicate lifecycle notification: %+v", duplicate)
					default:
					}
				})
			}
		}
	}
}
