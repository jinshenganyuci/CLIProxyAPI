package openai

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

type v820BoundaryHost struct {
	*issue6332LifecycleHost
	before, after atomic.Int32
}

func (*v820BoundaryHost) HasRequestInterceptors() bool { return true }
func (h *v820BoundaryHost) InterceptRequestBeforeAuth(_ context.Context, _ pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.before.Add(1)
	return pluginapi.RequestInterceptResponse{}
}
func (h *v820BoundaryHost) InterceptRequestAfterAuth(_ context.Context, _ pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.after.Add(1)
	return pluginapi.RequestInterceptResponse{}
}
func (*v820BoundaryHost) InterceptResponse(_ context.Context, _ pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}

type v820BoundaryUsage struct {
	provider string
	records  chan usage.Record
}

func (p *v820BoundaryUsage) HandleUsage(_ context.Context, record usage.Record) {
	if record.Provider == p.provider {
		p.records <- record
	}
}
func v820Await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("boundary notification missing")
		var zero T
		return zero
	}
}
func v820RegisterUsage(t *testing.T, provider string) *v820BoundaryUsage {
	t.Helper()
	capture := &v820BoundaryUsage{provider: provider, records: make(chan usage.Record, 32)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), responsesUsageNop{}) })
	return capture
}
func v820AssertNoMoreUsage(t *testing.T, capture *v820BoundaryUsage) {
	t.Helper()
	usage.PublishRecord(t.Context(), usage.Record{Provider: capture.provider, Model: "barrier"})
	if record := v820Await(t, capture.records); record.Model != "barrier" {
		t.Fatalf("duplicate accounting: %+v", record)
	}
}
func v820Router(base *handlers.BaseAPIHandler) *gin.Engine {
	router := gin.New()
	router.Use(logging.CPATraceIDMiddleware(), func(c *gin.Context) {
		c.Set("userApiKey", strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		c.Next()
	})
	chat := NewOpenAIAPIHandler(base)
	router.POST("/v1/audio/speech", chat.AudioSpeech)
	router.POST("/v1/tts", chat.XAITTS)
	router.POST("/v1/chat/completions", chat.ChatCompletions)
	router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
	return router
}
func v820Request(router http.Handler, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	writer := httptest.NewRecorder()
	router.ServeHTTP(writer, request)
	return writer
}

func TestV820SpeechThreeCredentialsThreeKeysKeepProxyAdmissionAndLifecycle(t *testing.T) {
	const provider = "xai"
	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		http.Error(w, "unexpected fallback", 502)
	}))
	t.Cleanup(fallback.Close)
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL, PassthroughHeaders: true}}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(runtimeexecutor.NewXAIExecutor(cfg))
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	entered := make(chan string, 3)
	var proxyHits atomic.Int32
	limits := []int{3, 3, 10}
	ids := make([]string, 3)
	for i := range 3 {
		id := fmt.Sprintf("%s-%d", t.Name(), i)
		ids[i] = id
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proxyHits.Add(1)
			if !r.URL.IsAbs() || r.URL.Host != "v820-speech.invalid" || r.URL.Path != "/v1/tts" || r.Header.Get("Authorization") != fmt.Sprintf("Bearer synthetic-upstream-%d", i) || r.Header.Get("Accept") != "*/*" {
				t.Errorf("credential proxy/header binding lost: %s", r.URL)
			}
			body, _ := io.ReadAll(r.Body)
			marker := gjson.GetBytes(body, "text").String()
			if marker == "unknown-voice" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"error":{"message":"unknown voice id"}}`)
				return
			}
			if gjson.GetBytes(body, "model").Exists() || gjson.GetBytes(body, "voice_id").String() != "eve" {
				t.Errorf("speech translation changed: %s", body)
			}
			entered <- id
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Speech-Proof", id)
			_, _ = io.WriteString(w, "AUDIO:"+marker)
		}))
		t.Cleanup(proxy.Close)
		metadata := map[string]any{"codex_identity_namespace": "preserved", "opaque_plugin": map[string]any{"enabled": false}}
		if i < 2 {
			metadata["concurrency_limit"] = limits[i]
		}
		auth, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: provider, Status: coreauth.StatusActive, ProxyURL: proxy.URL, Attributes: map[string]string{"api_key": fmt.Sprintf("synthetic-upstream-%d", i), "base_url": "http://v820-speech.invalid/v1", "using_api": "true"}, Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: "grok-tts"}, {ID: "grok-voice-tts-1.0"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		manager.RefreshSchedulerEntry(id)
		for range limits[i] - 1 {
			done, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), auth)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			t.Cleanup(done)
		}
	}
	t.Cleanup(unblock)
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	host := &v820BoundaryHost{issue6332LifecycleHost: &issue6332LifecycleHost{completions: make(chan pluginapi.RequestCompletion, 10)}}
	base.SetPluginHost(host)
	capture := v820RegisterUsage(t, provider)
	router := v820Router(base)
	type response struct {
		index    int
		recorder *httptest.ResponseRecorder
	}
	results := make(chan response, 3)
	formats := []string{"mp3", "wav", "pcm"}
	for i := range 3 {
		go func() {
			path := "/v1/audio/speech"
			if i == 2 {
				path = "/v1/tts"
			}
			body := fmt.Sprintf(`{"model":"tts-1","input":"request-%d","voice":"nova","response_format":%q,"stream":true}`, i, formats[i])
			results <- response{i, v820Request(router, path, body, fmt.Sprintf("synthetic-client-%d", i))}
		}()
	}
	seen := map[string]bool{}
	for range 3 {
		seen[v820Await(t, entered)] = true
	}
	if len(seen) != 3 {
		t.Fatalf("concurrent requests failed to occupy three credentials: %v", seen)
	}
	for i, id := range ids {
		if active, limit := manager.CredentialConcurrencySnapshot(id); active != limits[i] || limit != limits[i] {
			t.Fatalf("speech admission %s = %d/%d", id, active, limit)
		}
		_, err := manager.Execute(t.Context(), []string{provider}, coreexecutor.Request{Model: "grok-tts"}, coreexecutor.Options{Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: id}})
		if !coreauth.IsCredentialConcurrencyError(err) {
			t.Fatalf("saturated pinned speech credential escaped budget: %v", err)
		}
	}
	unblock()
	for range 3 {
		got := v820Await(t, results)
		w := got.recorder
		if w.Code != 200 || w.Body.String() != fmt.Sprintf("AUDIO:request-%d", got.index) || w.Header().Get("Content-Type") != speechContentType(formats[got.index]) || w.Header().Get("Content-Length") != "" || !seen[w.Header().Get("X-Speech-Proof")] {
			t.Fatalf("audio bytes/header contract changed: %d %v %q", w.Code, w.Header(), w.Body.String())
		}
	}
	usageKeys := map[string]bool{}
	for range 3 {
		completion := v820Await(t, host.completions)
		if completion.Outcome != pluginapi.RequestCompletionSucceeded {
			t.Fatalf("speech lifecycle failed: %+v", completion)
		}
		record := v820Await(t, capture.records)
		if record.Failed || record.Stream || record.Detail.TotalTokens != 0 || !seen[record.AuthID] {
			t.Fatalf("speech accounting not attributed once: %+v", record)
		}
		usageKeys[record.APIKey] = true
	}
	for i := range 3 {
		if !usageKeys[fmt.Sprintf("synthetic-client-%d", i)] {
			t.Fatalf("client accounting crossed keys: %v", usageKeys)
		}
	}
	for i, id := range ids {
		if active, limit := manager.CredentialConcurrencySnapshot(id); active != limits[i]-1 || limit != limits[i] {
			t.Fatalf("speech lease leaked: %d/%d", active, limit)
		}
	}
	errorResponse := v820Request(router, "/v1/audio/speech", `{"input":"unknown-voice"}`, "synthetic-client-0")
	if errorResponse.Code != 404 || !gjson.ValidBytes(errorResponse.Body.Bytes()) || errorResponse.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("speech error was returned as audio: %d %v %s", errorResponse.Code, errorResponse.Header(), errorResponse.Body.String())
	}
	if completion := v820Await(t, host.completions); completion.Outcome != pluginapi.RequestCompletionFailed {
		t.Fatalf("speech failure lifecycle lost: %+v", completion)
	}
	if record := v820Await(t, capture.records); !record.Failed || record.Detail.TotalTokens != 0 {
		t.Fatalf("unknown voice counted as success: %+v", record)
	}
	for _, id := range ids {
		auth, _ := manager.GetByID(id)
		if auth.Unavailable || auth.Status != coreauth.StatusActive {
			t.Fatalf("request-scoped speech error cooled credential: %+v", auth)
		}
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		w := v820Request(router, path, `{"model":"grok-tts","input":"hello","messages":[{"role":"user","content":"hello"}],"stream":true}`, "synthetic-client-1")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "/v1/audio/speech") {
			t.Fatalf("speech-only model escaped ordinary route: %d %s", w.Code, w.Body.String())
		}
	}
	if proxyHits.Load() != 4 || fallbackHits.Load() != 0 || host.before.Load() != 4 || host.after.Load() != 4 {
		t.Fatalf("speech proxy/interceptor call counts: proxy=%d fallback=%d before=%d after=%d", proxyHits.Load(), fallbackHits.Load(), host.before.Load(), host.after.Load())
	}
	v820AssertNoMoreUsage(t, capture)
	select {
	case duplicate := <-host.completions:
		t.Fatalf("duplicate completion: %+v", duplicate)
	default:
	}
}

func TestV820UnsupportedUserTurnFailsBeforeUpstreamAtHTTPBoundary(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", path, stream), func(t *testing.T) {
				var hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					http.Error(w, "conversion should reject before upstream", 502)
				}))
				t.Cleanup(upstream.Close)
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: upstream.URL}}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(2, 2, 0)
				manager.RegisterExecutor(runtimeexecutor.NewGeminiExecutor(cfg))
				ids := []string{t.Name() + "-a", t.Name() + "-b"}
				for _, id := range ids {
					_, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: "gemini", Status: coreauth.StatusActive, ProxyURL: upstream.URL, Attributes: map[string]string{"api_key": "synthetic", "base_url": "http://v820-invalid.invalid"}})
					if err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(id, "gemini", []*registry.ModelInfo{{ID: "v820-invalid-model"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}
				base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
				host := &v820BoundaryHost{issue6332LifecycleHost: &issue6332LifecycleHost{completions: make(chan pluginapi.RequestCompletion, 4)}}
				base.SetPluginHost(host)
				capture := v820RegisterUsage(t, "gemini")
				body := fmt.Sprintf(`{"model":"v820-invalid-model","stream":%t,"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-synthetic"}}]}]}`, stream)
				if path == "/v1/responses" {
					body = fmt.Sprintf(`{"model":"v820-invalid-model","stream":%t,"input":[{"role":"user","content":[{"type":"input_file","file_id":"file-synthetic"}]}]}`, stream)
				}
				result := v820Request(v820Router(base), path, body, "synthetic-invalid-client")
				if result.Code != 400 || !gjson.ValidBytes(result.Body.Bytes()) || !strings.Contains(result.Body.String(), "unsupported content part") {
					t.Fatalf("translation error did not reach HTTP boundary: %d %s", result.Code, result.Body.String())
				}
				completion := v820Await(t, host.completions)
				if completion.Outcome != pluginapi.RequestCompletionFailed || completion.StatusCode != 400 {
					t.Fatalf("conversion completion changed: %+v", completion)
				}
				record := v820Await(t, capture.records)
				if !record.Failed || record.Fail.StatusCode != 400 || record.Detail.TotalTokens != 0 {
					t.Fatalf("pre-upstream rejection invented usage: %+v", record)
				}
				if hits.Load() != 0 || host.before.Load() != 1 || host.after.Load() != 1 {
					t.Fatalf("invalid turn retried or reached upstream: hits=%d before=%d after=%d", hits.Load(), host.before.Load(), host.after.Load())
				}
				for _, id := range ids {
					auth, _ := manager.GetByID(id)
					active, limit := manager.CredentialConcurrencySnapshot(id)
					if active != 0 || limit != 10 || auth.Unavailable || auth.Status != coreauth.StatusActive {
						t.Fatalf("invalid turn leaked admission or cooled credential: %d/%d %+v", active, limit, auth)
					}
				}
				v820AssertNoMoreUsage(t, capture)
				select {
				case duplicate := <-host.completions:
					t.Fatalf("duplicate conversion completion: %+v", duplicate)
				default:
				}
			})
		}
	}
}

func TestV820CommittedSSECompactsPrettyUpstreamErrorWithoutLosingFields(t *testing.T) {
	const provider = "v820-sse-compat"
	const model = "v820-sse-model"
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"sse\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first payload\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "event: error\ndata: {\ndata:   \"error\": {\ndata:     \"status_code\": 422,\ndata:     \"message\": \"synthetic validation error\",\ndata:     \"details\": {\"field\": \"input\", \"retryable\": false}\ndata:   }\ndata: }\n\n")
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(release)
	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, cfg))
	id := t.Name()
	_, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: provider, Status: coreauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": upstream.URL}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	host := &v820BoundaryHost{issue6332LifecycleHost: &issue6332LifecycleHost{completions: make(chan pluginapi.RequestCompletion, 4)}}
	base.SetPluginHost(host)
	capture := v820RegisterUsage(t, provider)
	gateway := httptest.NewServer(v820Router(base))
	t.Cleanup(gateway.Close)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, gateway.URL+"/v1/chat/completions", strings.NewReader(`{"model":"v820-sse-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	reader := bufio.NewReader(response.Body)
	var wire strings.Builder
	for {
		line, errRead := reader.ReadString('\n')
		if errRead != nil {
			t.Fatal(errRead)
		}
		wire.WriteString(line)
		if line == "\n" && strings.Contains(wire.String(), "first payload") {
			break
		}
	}
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("initial stream was not committed: %d %v", response.StatusCode, response.Header)
	}
	release()
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	wire.Write(rest)
	errorsSeen := 0
	for _, line := range strings.Split(wire.String(), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			continue
		}
		if !gjson.Valid(payload) {
			t.Fatalf("pretty error split into invalid SSE data line: %q", line)
		}
		if gjson.Get(payload, "error").Exists() {
			errorsSeen++
			if gjson.Get(payload, "error.status_code").Int() != 422 || gjson.Get(payload, "error.details.field").String() != "input" || gjson.Get(payload, "error.details.retryable").Raw != "false" {
				t.Fatalf("SSE compaction lost structured error: %s", payload)
			}
		}
	}
	if errorsSeen != 1 {
		t.Fatalf("terminal errors=%d, wire=%s", errorsSeen, wire.String())
	}
	if completion := v820Await(t, host.completions); completion.Outcome != pluginapi.RequestCompletionFailed || completion.StatusCode != 422 {
		t.Fatalf("committed error completion changed: %+v", completion)
	}
	if record := v820Await(t, capture.records); !record.Failed || record.Fail.StatusCode != 422 {
		t.Fatalf("committed error usage changed: %+v", record)
	}
	v820AssertNoMoreUsage(t, capture)
}
