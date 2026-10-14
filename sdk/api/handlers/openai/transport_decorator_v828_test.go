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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type v828RoundTripFunc func(*http.Request) (*http.Response, error)

func (f v828RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type v828DecoratorHost struct {
	*issue6332LifecycleHost
	manager *coreauth.Manager
	test    *testing.T
	ids     map[string]string
	limits  map[string]int
	stages  map[string]*atomic.Int32
}

func (*v828DecoratorHost) HasRequestInterceptors() bool { return true }
func (h *v828DecoratorHost) InterceptRequestBeforeAuth(ctx context.Context, _ pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	caller := usage.APIKeyFromContext(ctx)
	if stage := h.stages[caller]; stage == nil || stage.Add(1) != 1 {
		h.test.Errorf("before-auth caller identity or invocation changed: %q", caller)
	}
	return pluginapi.RequestInterceptResponse{}
}
func (h *v828DecoratorHost) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	caller := usage.APIKeyFromContext(ctx)
	id, _ := req.Metadata[coreexecutor.SelectedAuthMetadataKey].(string)
	active, limit := h.manager.CredentialConcurrencySnapshot(id)
	if id != h.ids[caller] || active != h.limits[caller] || limit != h.limits[caller] {
		h.test.Errorf("after-auth changed selected caller admission: %q %q %d/%d", caller, id, active, limit)
	}
	if stage := h.stages[caller]; stage == nil || stage.Add(1) != 2 {
		h.test.Errorf("after-auth invocation changed: %q", caller)
	}
	return pluginapi.RequestInterceptResponse{}
}
func (*v828DecoratorHost) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}

func TestV828TransportDecoratorStaysRequestScopedAcrossHooksProxyAndCancellation(t *testing.T) {
	const provider = "v828-decorator-provider"
	var globalHits atomic.Int32
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		globalHits.Add(1)
		http.Error(w, "unexpected global fallback", 502)
	}))
	t.Cleanup(global.Close)
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, cfg))
	host := &v828DecoratorHost{issue6332LifecycleHost: &issue6332LifecycleHost{completions: make(chan pluginapi.RequestCompletion, 4)}, manager: manager, test: t, ids: map[string]string{}, limits: map[string]int{}, stages: map[string]*atomic.Int32{}}
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	base.SetPluginHost(host)
	capture := v820RegisterUsage(t, provider)
	type requestState struct {
		caller, model, authID string
		limit                 int
		entered, finished     chan struct{}
		cancel                context.CancelFunc
		writer                *httptest.ResponseRecorder
		wrapped, observed     atomic.Int32
		proxyCalls            atomic.Int32
	}
	states := make([]*requestState, 0, 3)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	for i, limit := range []int{3, 2, 10} {
		state := &requestState{caller: fmt.Sprintf("synthetic-v828-caller-%d", i), model: fmt.Sprintf("v828-decorator-model-%d", i), authID: fmt.Sprintf("%s-auth-%d", t.Name(), i), limit: limit, entered: make(chan struct{}), finished: make(chan struct{}), writer: httptest.NewRecorder()}
		states = append(states, state)
		host.ids[state.caller], host.limits[state.caller], host.stages[state.caller] = state.authID, limit, &atomic.Int32{}
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			state.proxyCalls.Add(1)
			if !r.URL.IsAbs() || r.URL.Host != "v828-upstream.invalid" || r.Header.Get("Authorization") != "Bearer "+state.authID {
				t.Errorf("decorator changed selected transport/credential: %s", r.URL)
			}
			body, _ := io.ReadAll(r.Body)
			close(state.entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			if gjson.GetBytes(body, "stream").Bool() {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"v828\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
			} else {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"v828","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
			}
		}))
		t.Cleanup(proxy.Close)
		auth := &coreauth.Auth{ID: state.authID, Provider: provider, Status: coreauth.StatusActive, ProxyURL: proxy.URL, Attributes: map[string]string{"api_key": state.authID, "base_url": "http://v828-upstream.invalid/v1"}}
		if i != 2 {
			auth.Metadata = map[string]any{"concurrency_limit": limit}
		}
		registered, err := manager.Register(t.Context(), auth)
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(state.authID, provider, []*registry.ModelInfo{{ID: state.model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(state.authID) })
		for range limit - 1 {
			releaseSlot, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), registered)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			t.Cleanup(releaseSlot)
		}
	}
	t.Cleanup(unblock)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userApiKey", c.GetHeader("X-V828-Caller"))
		c.Set("accessProvider", "access-guard")
		c.Next()
	})
	router.POST("/v1/chat/completions", NewOpenAIAPIHandler(base).ChatCompletions)
	for i, state := range states {
		ctx, cancel := context.WithCancel(t.Context())
		state.cancel = cancel
		t.Cleanup(cancel)
		if i != 1 {
			ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
				state.wrapped.Add(1)
				if host.stages[state.caller].Load() != 2 {
					t.Errorf("transport selected before request hooks completed: %s", state.caller)
				}
				return v828RoundTripFunc(func(req *http.Request) (*http.Response, error) {
					state.observed.Add(1)
					if usage.APIKeyFromContext(req.Context()) != state.caller || req.Header.Get("Authorization") != "Bearer "+state.authID {
						t.Error("request-scoped decorator observed another caller or pre-auth headers")
					}
					return next.RoundTrip(req)
				})
			})
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, state.model, i != 1))).WithContext(ctx)
		request.Header.Set("X-V828-Caller", state.caller)
		go func() {
			defer close(state.finished)
			router.ServeHTTP(state.writer, request)
		}()
	}
	for _, state := range states {
		v820Await(t, state.entered)
	}
	states[2].cancel()
	v820Await(t, states[2].finished)
	unblock()
	for _, state := range states {
		v820Await(t, state.finished)
	}
	records := map[string]usage.Record{}
	for range states {
		record := v820Await(t, capture.records)
		if _, exists := records[record.AuthID]; exists {
			t.Fatalf("duplicate usage for %s", record.AuthID)
		}
		records[record.AuthID] = record
	}
	v820AssertNoMoreUsage(t, capture)
	for range states {
		completion := v820Await(t, host.completions)
		if completion.Model == states[2].model && completion.Outcome == pluginapi.RequestCompletionSucceeded {
			t.Fatal("canceled decorated request completed as success")
		}
	}
	for i, state := range states {
		wantWrapped := int32(1)
		if i == 1 {
			wantWrapped = 0
		}
		if state.wrapped.Load() != wantWrapped || state.observed.Load() != wantWrapped || state.proxyCalls.Load() != 1 || host.stages[state.caller].Load() != 2 {
			t.Fatalf("decorator/hooks crossed caller boundary: %s wraps=%d observed=%d proxy=%d stages=%d", state.caller, state.wrapped.Load(), state.observed.Load(), state.proxyCalls.Load(), host.stages[state.caller].Load())
		}
		record, exists := records[state.authID]
		wantTokens := int64(5)
		if i == 2 {
			wantTokens = 0
		}
		if !exists || record.APIKey != state.caller || record.AccessProvider != "access-guard" || record.IsNativeKey || record.Failed != (i == 2) || record.Detail.TotalTokens != wantTokens {
			t.Fatalf("decorator changed usage identity or delivery: %+v", record)
		}
		if i != 2 && (state.writer.Code != http.StatusOK || !strings.Contains(state.writer.Body.String(), "ok")) {
			t.Fatalf("decorator consumed or changed response: %d %s", state.writer.Code, state.writer.Body.String())
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			active, limit := manager.CredentialConcurrencySnapshot(state.authID)
			if active == state.limit-1 && limit == state.limit {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("decorated request leaked lease: %d/%d", active, limit)
			}
			runtime.Gosched()
		}
	}
	if globalHits.Load() != 0 {
		t.Fatalf("decorator bypassed credential proxy: global hits=%d", globalHits.Load())
	}
}
