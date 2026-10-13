package openai

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type v827PreFTExecutor struct {
	entered chan struct{}
}

func TestV827PreFirstTokenCommitKeepsProxyErrorAndPluginAccounting(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			const model = "v827-proxy-preft"
			const body = `{"error":{"message":"synthetic pre-ft policy","type":"invalid_request_error","code":"unprocessable_entity","details":{"fixture":true}}}`
			var calls, fallbackCalls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if !r.URL.IsAbs() || r.URL.Host != "v827-upstream.invalid" || r.Header.Get("Authorization") != "Bearer synthetic-v827-token" {
					t.Errorf("request escaped credential proxy: %s", r.URL)
				}
				_, _ = io.Copy(io.Discard, r.Body)
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = io.WriteString(w, body)
			}))
			t.Cleanup(proxy.Close)
			t.Cleanup(unblock)
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackCalls.Add(1)
				http.Error(w, "unexpected global proxy", 502)
			}))
			t.Cleanup(fallback.Close)
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}}
			cfg.Streaming.KeepAliveSeconds = 10
			cfg.Streaming.SendKeepAliveBeforeFT = enabled
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor("v827-compat", cfg))
			id := t.Name()
			auth, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: "v827-compat", Status: coreauth.StatusActive, ProxyURL: proxy.URL, Attributes: map[string]string{"base_url": "http://v827-upstream.invalid/v1", "api_key": "synthetic-v827-token"}})
			if err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(id, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			for range 9 {
				releaseSlot, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), auth)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				t.Cleanup(releaseSlot)
			}
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			host := &v815DeliveryHost{issue6332LifecycleHost: &issue6332LifecycleHost{completions: make(chan pluginapi.RequestCompletion, 2)}, manager: manager, wantAuthID: id, test: t}
			base.SetPluginHost(host)
			capture := &issue6332Capture{records: make(chan usage.Record, 2), authID: id}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), responsesUsageNop{}) })
			ticks := make(chan time.Time)
			restore := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} })
			defer restore()
			writer := &v827KeepAliveWriter{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
			router := gin.New()
			router.POST("/v1/chat/completions", NewOpenAIAPIHandler(base).ChatCompletions)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				router.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"v827-proxy-preft","messages":[{"role":"user","content":"hello"}],"stream":true}`)))
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not reach its credential proxy")
			}
			if enabled {
				ticks <- time.Unix(1, 0)
				<-writer.wrote
			}
			unblock()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream failure did not complete the handler")
			}
			got := writer.Body.String()
			wantStatus := http.StatusUnprocessableEntity
			if enabled {
				wantStatus = http.StatusOK
				if !strings.Contains(got, ": keep-alive\n\n") || strings.Count(got, "data: ") != 1 || strings.Contains(got, "[DONE]") {
					t.Fatalf("committed stream lost its unique terminal error: %s", got)
				}
				got = strings.TrimSpace(strings.SplitN(got, "data: ", 2)[1])
			}
			if writer.Code != wantStatus || gjson.Get(got, "error.code").String() != "unprocessable_entity" || !gjson.Get(got, "error.details.fixture").Bool() {
				t.Fatalf("bootstrap error shape/status changed: %d %s", writer.Code, got)
			}
			select {
			case completion := <-host.completions:
				if completion.Outcome != pluginapi.RequestCompletionFailed || completion.StatusCode != http.StatusUnprocessableEntity {
					t.Fatalf("plugin accounting followed downstream 200 instead of upstream failure: %+v", completion)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("plugin completion missing")
			}
			select {
			case record := <-capture.records:
				if !record.Failed || record.Detail.TotalTokens != 0 {
					t.Fatalf("keepalive created a successful billable usage record: %+v", record)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("upstream failure usage missing")
			}
			if active, limit := manager.CredentialConcurrencySnapshot(id); active != 9 || limit != 10 || calls.Load() != 1 || fallbackCalls.Load() != 0 {
				t.Fatalf("bootstrap changed proxy or lease: slots=%d/%d calls=%d fallback=%d", active, limit, calls.Load(), fallbackCalls.Load())
			}
		})
	}
}

func (*v827PreFTExecutor) Identifier() string { return "v827-preft" }
func (e *v827PreFTExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	_, err := e.ExecuteStream(ctx, auth, req, opts)
	return coreexecutor.Response{}, err
}
func (e *v827PreFTExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (_ *coreexecutor.StreamResult, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	close(e.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*v827PreFTExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (*v827PreFTExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}
func (*v827PreFTExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type v827KeepAliveWriter struct {
	*httptest.ResponseRecorder
	mode  string
	once  sync.Once
	wrote chan struct{}
}

func (w *v827KeepAliveWriter) Write(payload []byte) (int, error) {
	if bytes.Contains(payload, []byte(": keep-alive")) {
		w.once.Do(func() { close(w.wrote) })
		switch w.mode {
		case "write-error":
			return 0, io.ErrClosedPipe
		case "short-write":
			return len(payload) - 1, nil
		}
	}
	return w.ResponseRecorder.Write(payload)
}
func (w *v827KeepAliveWriter) Flush() { _ = w.FlushError() }
func (w *v827KeepAliveWriter) FlushError() error {
	if w.mode == "flush-error" {
		return io.ErrClosedPipe
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestV827PreFirstTokenDeliveryFailureCancelsBootstrapAndReleasesLease(t *testing.T) {
	for _, endpoint := range []string{"chat", "completions", "responses", "claude", "gemini", "interactions", "images-routed", "images-compat", "images-responses", "images-nonstream"} {
		for _, mode := range []string{"write-error", "short-write", "flush-error"} {
			t.Run(endpoint+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ticks := make(chan time.Time)
				restoreTicker := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) {
					return ticks, func() {}
				})
				defer restoreTicker()
				cfg := &config.Config{}
				cfg.Streaming.KeepAliveSeconds = 10
				cfg.Streaming.SendKeepAliveBeforeFT = true
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				exec := &v827PreFTExecutor{entered: make(chan struct{})}
				manager.RegisterExecutor(exec)
				id, model := t.Name(), "v827-preft-model"
				auth, err := manager.Register(ctx, &coreauth.Auth{ID: id, Provider: exec.Identifier(), Status: coreauth.StatusActive})
				if err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				for range 9 {
					release, errAcquire := manager.AcquireCredentialConcurrency(ctx, auth)
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					t.Cleanup(release)
				}
				base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
				host := &v815DeliveryHost{issue6332LifecycleHost: &issue6332LifecycleHost{completions: make(chan pluginapi.RequestCompletion, 2)}, manager: manager, wantAuthID: id, test: t}
				base.SetPluginHost(host)
				capture := &issue6332Capture{records: make(chan usage.Record, 2), authID: id}
				usage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), responsesUsageNop{}) })
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set("userApiKey", "synthetic-v827-caller")
					c.Set("accessProvider", "access-guard")
					c.Next()
				})
				path := "/stream"
				body := `{"model":"v827-preft-model","stream":true,"input":"hello","messages":[{"role":"user","content":"hello"}],"prompt":"hello","contents":[{"parts":[{"text":"hello"}]}]}`
				switch endpoint {
				case "chat":
					router.POST(path, NewOpenAIAPIHandler(base).ChatCompletions)
				case "completions":
					router.POST(path, NewOpenAIAPIHandler(base).Completions)
				case "responses":
					router.POST(path, NewOpenAIResponsesAPIHandler(base).Responses)
				case "claude":
					router.POST(path, claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
				case "gemini":
					path = "/v1beta/models/v827-preft-model:streamGenerateContent"
					router.POST("/v1beta/models/*action", gemini.NewGeminiAPIHandler(base).GeminiHandler)
				case "interactions":
					router.POST(path, gemini.NewGeminiAPIHandler(base).Interactions)
				case "images-routed":
					router.POST(path, func(c *gin.Context) {
						NewOpenAIAPIHandler(base).streamRoutedImages(c, []byte(body), model)
					})
				case "images-compat":
					router.POST(path, func(c *gin.Context) {
						NewOpenAIAPIHandler(base).streamOpenAICompatImages(c, []byte(body), model)
					})
				case "images-responses":
					router.POST(path, func(c *gin.Context) {
						NewOpenAIAPIHandler(base).streamImagesFromResponses(c, []byte(body), "b64_json", "image_generation")
					})
				case "images-nonstream":
					router.POST(path, func(c *gin.Context) {
						NewOpenAIAPIHandler(base).streamImagesWithModel(c, []byte(body), model, "b64_json", "image_generation")
					})
				}
				writer := &v827KeepAliveWriter{ResponseRecorder: httptest.NewRecorder(), mode: mode, wrote: make(chan struct{})}
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
					router.ServeHTTP(writer, request)
				}()
				defer func() {
					cancel()
					select {
					case <-finished:
					case <-time.After(5 * time.Second):
						t.Error("handler did not finish after cleanup cancellation")
					}
				}()
				select {
				case <-exec.entered:
				case <-finished:
					t.Fatalf("request rejected before bootstrap: %d %s", writer.Code, writer.Body.String())
				case <-time.After(5 * time.Second):
					t.Fatal("bootstrap executor was not reached")
				}
				if active, limit := manager.CredentialConcurrencySnapshot(id); active != 10 || limit != 10 {
					t.Fatalf("bootstrap admission = %d/%d, want 10/10", active, limit)
				}
				ticks <- time.Unix(1, 0)
				<-writer.wrote
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("failed first keepalive left bootstrap running and credential occupied")
				}
				select {
				case completion := <-host.completions:
					if completion.Outcome == pluginapi.RequestCompletionSucceeded || completion.Error == "" {
						t.Fatalf("failed delivery was counted as plugin success: %+v", completion)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("plugin completion missing")
				}
				select {
				case record := <-capture.records:
					if !record.Failed || record.Detail.TotalTokens != 0 || record.APIKey != "synthetic-v827-caller" || record.AccessProvider != "access-guard" || record.IsNativeKey {
						t.Fatalf("failed bootstrap usage identity/outcome changed: %+v", record)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("failed bootstrap usage missing")
				}
				deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
				defer stop()
				for {
					active, limit := manager.CredentialConcurrencySnapshot(id)
					if active == 9 && limit == 10 {
						break
					}
					if deadline.Err() != nil {
						t.Fatalf("failed keepalive leaked a credential slot: %d/%d", active, limit)
					}
					runtime.Gosched()
				}
				if host.before.Load() != 1 || host.after.Load() != 1 {
					t.Fatalf("plugin hooks repeated: before=%d after=%d", host.before.Load(), host.after.Load())
				}
			})
		}
	}
}
