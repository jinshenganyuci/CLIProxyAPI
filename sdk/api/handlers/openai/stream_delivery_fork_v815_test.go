package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type v815DeliveryHost struct {
	*issue6332LifecycleHost
	manager       *coreauth.Manager
	wantAuthID    string
	test          *testing.T
	before, after atomic.Int32
}

type v815NativeDeliveryExecutor struct {
	initialFailureStreamExecutor
	provider  string
	delivered chan error
}

func (e *v815NativeDeliveryExecutor) Identifier() string { return e.provider }

func (e *v815NativeDeliveryExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	stop := usage.SupportStreamDelivery(ctx)
	chunks := make(chan coreexecutor.StreamChunk, 3)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"codex.response.metadata\",\"headers\":{\"x-codex-turn-state\":\"opaque-turn-state\"}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"item-one\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"collected\"}]}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-one\",\"status\":\"completed\",\"output\":[]}}\n\n")}
	close(chunks)
	go func() {
		defer stop()
		errDelivery, _ := usage.WaitStreamDelivery(ctx)
		e.delivered <- errDelivery
	}()
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestV815DeliveryMergePreservesSelectedNativeCodexOutput(t *testing.T) {
	for _, provider := range []string{"codex", "v815-other-provider"} {
		t.Run(provider, func(t *testing.T) {
			const model = "v815-native-output-model"
			executor := &v815NativeDeliveryExecutor{provider: provider, delivered: make(chan error, 1)}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			auth, errRegister := manager.Register(t.Context(), &coreauth.Auth{ID: t.Name(), Provider: provider, Status: coreauth.StatusActive, ProxyURL: "direct"})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)
			completions := make(chan pluginapi.RequestCompletion, 2)
			base.SetPluginHost(&issue6332LifecycleHost{completions: completions})
			router := gin.New()
			router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"v815-native-output-model","stream":true,"input":"hello"}`))
			request.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
			writer := httptest.NewRecorder()
			router.ServeHTTP(writer, request)
			if got := strings.Contains(writer.Body.String(), "opaque-turn-state"); got != (provider == "codex") {
				t.Fatalf("native metadata did not follow the selected provider: %s", writer.Body.String())
			}
			seen := false
			for _, line := range strings.Split(writer.Body.String(), "\n") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if gjson.Get(payload, "type").String() == "response.completed" {
					seen = true
					want := int64(1)
					if provider == "codex" {
						want = 0
					}
					if got := gjson.Get(payload, "response.output.#").Int(); got != want {
						t.Fatalf("selected native output was repaired incorrectly: %s", payload)
					}
				}
			}
			if !seen {
				t.Fatal("terminal completion was not delivered")
			}
			select {
			case errDelivery := <-executor.delivered:
				if errDelivery != nil {
					t.Fatal(errDelivery)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("delivery context was lost during native-output selection")
			}
			select {
			case completion := <-completions:
				if completion.Outcome != pluginapi.RequestCompletionSucceeded {
					t.Fatalf("native output lifecycle failed: %+v", completion)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("missing delivery lifecycle")
			}
		})
	}
}

func (*v815DeliveryHost) HasRequestInterceptors() bool { return true }

func (h *v815DeliveryHost) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.before.Add(1)
	return pluginapi.RequestInterceptResponse{}
}

func (h *v815DeliveryHost) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.after.Add(1)
	id, _ := req.Metadata[coreexecutor.SelectedAuthMetadataKey].(string)
	active, limit := h.manager.CredentialConcurrencySnapshot(id)
	if id != h.wantAuthID || active != 10 || limit != 10 {
		h.test.Errorf("plugin after-auth ran outside the selected credential admission: %q %d/%d", id, active, limit)
	}
	return pluginapi.RequestInterceptResponse{}
}

func (*v815DeliveryHost) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}

func TestV815StreamDeliveryPreservesCredentialProxyAdmissionAndPluginLifecycle(t *testing.T) {
	for _, mode := range []string{"delivered", "early-cancel", "write-error", "short-write", "flush-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			const model = "gemini-3.7-flash"
			id := t.Name() + "-a"
			otherID := t.Name() + "-z"
			var requests, globalRequests atomic.Int32
			global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				globalRequests.Add(1)
				http.Error(w, "unexpected global or alternate route", http.StatusBadGateway)
			}))
			t.Cleanup(global.Close)
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if !r.URL.IsAbs() || r.URL.Host != "v815-upstream.invalid" || r.Header.Get("Authorization") != "Bearer synthetic-selected-token" {
					t.Errorf("selected credential proxy was bypassed: %s", r.URL)
				}
				_, _ = io.Copy(io.Discard, r.Body)
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				finish := `,"finishReason":"STOP"`
				if mode == "early-cancel" {
					finish = ""
				}
				_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}`+finish+`}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33},"responseId":"v815-response"}}`+"\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			t.Cleanup(proxy.Close)
			t.Cleanup(unblock)
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL}}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))
			var selected *coreauth.Auth
			for _, authID := range []string{id, otherID} {
				proxyURL := global.URL
				if authID == id {
					proxyURL = proxy.URL
				}
				auth, errRegister := manager.Register(ctx, &coreauth.Auth{
					ID: authID, Provider: "antigravity", Status: coreauth.StatusActive, ProxyURL: proxyURL,
					Attributes: map[string]string{"base_url": "http://v815-upstream.invalid"},
					Metadata:   map[string]any{"access_token": "synthetic-selected-token", "expired": "2099-01-01T00:00:00Z", "project_id": "synthetic-project"},
				})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				if authID == id {
					selected = auth
				}
				registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			}
			for range 9 {
				done, errAcquire := manager.AcquireCredentialConcurrency(ctx, selected)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				t.Cleanup(done)
			}
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			host := &v815DeliveryHost{issue6332LifecycleHost: &issue6332LifecycleHost{completions: make(chan pluginapi.RequestCompletion, 4)}, manager: manager, wantAuthID: id, test: t}
			base.SetPluginHost(host)
			capture := &issue6332Capture{records: make(chan usage.Record, 4), authID: id}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), responsesUsageNop{}) })
			writer := &issue6332Writer{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, event: "response.completed", failWrite: mode == "write-error" || mode == "short-write", shortWrite: mode == "short-write", failFlush: mode == "flush-error"}
			if mode == "early-cancel" {
				writer.event = "response.output_text.delta"
			}
			router := gin.New()
			router.Use(logging.GinLogrusLogger(), logging.CPATraceIDMiddleware())
			router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"input":"hello"}`, model))).WithContext(ctx)
				router.ServeHTTP(writer, request)
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("request did not reach its credential-bound proxy")
			}
			if active, limit := manager.CredentialConcurrencySnapshot(id); active != 10 || limit != 10 {
				t.Fatalf("in-flight admission = %d/%d, want 10/10", active, limit)
			}
			_, errOverflow := manager.Execute(ctx, []string{"antigravity"}, coreexecutor.Request{Model: model}, coreexecutor.Options{Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: id}})
			if !coreauth.IsCredentialConcurrencyError(errOverflow) {
				t.Fatalf("pinned overflow did not preserve local concurrency rejection: %v", errOverflow)
			}
			unblock()
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
				t.Fatal("HTTP consumer did not finish")
			}
			wantOutcome := pluginapi.RequestCompletionFailed
			wantError := io.ErrClosedPipe.Error()
			switch mode {
			case "delivered":
				wantOutcome, wantError = pluginapi.RequestCompletionSucceeded, ""
			case "early-cancel":
				wantOutcome, wantError = pluginapi.RequestCompletionCanceled, context.Canceled.Error()
			case "short-write":
				wantError = io.ErrShortWrite.Error()
			}
			select {
			case completion := <-host.completions:
				if completion.Outcome != wantOutcome || completion.Error != wantError {
					t.Fatalf("plugin completion = %+v, want %s/%q", completion, wantOutcome, wantError)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("plugin lifecycle did not observe delivery acknowledgement")
			}
			select {
			case record := <-capture.records:
				if record.Failed != (mode != "delivered") || record.Detail.TotalTokens != 33 {
					t.Fatalf("delivery accounting changed: %+v", record)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("upstream accounting did not observe delivery acknowledgement")
			}
			// HTTP delivery, plugin completion and usage run in separate goroutines
			// from the manager's deferred lease release. Their notifications are
			// not a cleanup barrier, so wait for the actual budget to settle.
			cleanupCtx, stopCleanupWait := context.WithTimeout(t.Context(), 5*time.Second)
			for {
				active, limit := manager.CredentialConcurrencySnapshot(id)
				if active == 9 && limit == 10 {
					break
				}
				select {
				case <-cleanupCtx.Done():
					stopCleanupWait()
					t.Fatalf("terminal delivery leaked its credential lease: %d/%d", active, limit)
				default:
					runtime.Gosched()
				}
			}
			stopCleanupWait()
			if active, limit := manager.CredentialConcurrencySnapshot(otherID); active != 0 || limit != 10 {
				t.Fatalf("pinned overflow borrowed the alternate credential: %d/%d", active, limit)
			}
			if requests.Load() != 1 || globalRequests.Load() != 0 || host.before.Load() != 1 || host.after.Load() != 1 || !strings.Contains(writer.Header().Get(logging.CPATraceIDHeader), selected.Index) {
				t.Fatalf("proxy/plugin/trace boundaries changed: requests=%d fallback=%d before=%d after=%d trace=%s", requests.Load(), globalRequests.Load(), host.before.Load(), host.after.Load(), writer.Header().Get(logging.CPATraceIDHeader))
			}
			select {
			case duplicate := <-host.completions:
				t.Fatalf("duplicate plugin completion: %+v", duplicate)
			default:
			}
		})
	}
}
