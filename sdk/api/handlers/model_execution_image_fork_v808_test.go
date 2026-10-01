package handlers

import (
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

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Exercise native image/path support through the real admission and HTTP transport chain.
// The plugin host below observes the same lifecycle contract used by Access Guard;
// the release integration suite separately verifies the published plugin binary.
func TestV808ImagePathExecutionPreservesCredentialProxyConcurrencyAndLifecycle(t *testing.T) {
	for _, internal := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("internal=%t/stream=%t", internal, stream), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				release := make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				defer unblock()
				const model = "gpt-image-2.5"
				const provider = "v808-image-compat"
				limits := []int{10, 3, 10}
				ids := make([]string, len(limits))
				proxyURLs := make([]string, len(limits))
				arrived := make(chan int, 64)
				var networkCounts [3]atomic.Int32
				var globalCount atomic.Int32
				globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					globalCount.Add(1)
					http.Error(w, "unexpected global proxy", http.StatusBadGateway)
				}))
				t.Cleanup(globalProxy.Close)
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: globalProxy.URL}}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, cfg))
				for index, limit := range limits {
					index := index
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						networkCounts[index].Add(1)
						if r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != fmt.Sprintf("Bearer upstream-%d", index) {
							t.Errorf("credential %d received path=%q authorization=%q", index, r.URL.Path, r.Header.Get("Authorization"))
						}
						if r.URL.IsAbs() != (index != 1) {
							t.Errorf("credential %d transport absolute URL=%t", index, r.URL.IsAbs())
						}
						_, _ = io.Copy(io.Discard, r.Body)
						arrived <- index
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: {\"partial\":true}\n\n")
							w.(http.Flusher).Flush()
						}
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
						if stream {
							_, _ = io.WriteString(w, "data: {\"data\":[{\"b64_json\":\"fixture\"}]}\n\n")
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{"data":[{"b64_json":"fixture"}]}`)
						}
					}))
					t.Cleanup(server.Close)
					ids[index] = fmt.Sprintf("%s-credential-%d", t.Name(), index)
					baseURL := "http://v808-image.invalid/v1"
					proxyURLs[index] = server.URL
					if index == 1 {
						baseURL = server.URL + "/v1"
						proxyURLs[index] = "direct"
					}
					auth := &coreauth.Auth{
						ID: ids[index], Provider: provider, Status: coreauth.StatusActive,
						ProxyURL:   proxyURLs[index],
						Attributes: map[string]string{"base_url": baseURL, "api_key": fmt.Sprintf("upstream-%d", index)},
					}
					if limit != 10 {
						auth.Metadata = map[string]any{"concurrency_limit": limit}
					}
					if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				}
				handler := NewBaseAPIHandlers(&cfg.SDKConfig, manager)
				var mu sync.Mutex
				beforePaths := make(map[string]map[string]any)
				afterPaths := make(map[string]map[string]any)
				completions := make(map[string]pluginapi.RequestCompletion)
				keyCounts := make(map[string]int)
				var rejectStage atomic.Int32
				handler.SetPluginHost(&handlerInterceptorTestHost{
					interceptRequestBeforeAuth: func(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
						mu.Lock()
						defer mu.Unlock()
						if req.Metadata[coreexecutor.RequestPathMetadataKey] != "/v1/images/generations" {
							t.Errorf("initial path missing: %v", req.Metadata[coreexecutor.RequestPathMetadataKey])
						}
						beforePaths[req.RequestID] = req.Metadata
						keyCounts[req.Headers.Get("X-Test-Key")]++
						return pluginapi.RequestInterceptResponse{Path: " /v1/images/edits ", Terminate: rejectStage.Load() == 1, StatusCode: http.StatusForbidden}
					},
					interceptRequestAfterAuth: func(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
						mu.Lock()
						defer mu.Unlock()
						if req.Metadata[coreexecutor.RequestPathMetadataKey] != "/v1/images/edits" {
							t.Errorf("before-auth path was not propagated: %v", req.Metadata[coreexecutor.RequestPathMetadataKey])
						}
						id, _ := req.Metadata[coreexecutor.SelectedAuthMetadataKey].(string)
						if active, limit := manager.CredentialConcurrencySnapshot(id); active < 1 || active > limit {
							t.Errorf("after-auth interceptor ran without admission: %s %d/%d", id, active, limit)
						}
						afterPaths[req.RequestID] = req.Metadata
						return pluginapi.RequestInterceptResponse{Path: " /v1/images/generations ", Terminate: rejectStage.Load() == 2, StatusCode: http.StatusForbidden}
					},
					completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) {
						mu.Lock()
						defer mu.Unlock()
						if _, exists := completions[completion.RequestID]; exists {
							t.Errorf("duplicate terminal notification: %s", completion.RequestID)
						}
						completions[completion.RequestID] = completion
					},
				})
				execute := func(index, key int, requestProxy string) *interfaces.ErrorMessage {
					request := ModelExecutionRequest{
						EntryProtocol: "openai-image", ExitProtocol: "openai-image", Model: model,
						ForcedProvider: provider, AuthID: ids[index], ProxyURL: requestProxy,
						Path: " /v1/images/generations ", Stream: stream,
						Body:    []byte(`{"prompt":"synthetic fixture"}`),
						Headers: http.Header{"X-Test-Key": []string{fmt.Sprintf("key-%d", key)}},
					}
					protocol := ProtocolExecutionRequest{
						EntryProtocol: request.EntryProtocol, ExitProtocol: request.ExitProtocol, Model: model,
						ForcedProvider: provider, Path: request.Path, Stream: stream, Body: request.Body, Headers: request.Headers,
					}
					callCtx := WithPinnedAuthID(ctx, ids[index])
					if stream {
						var response ModelExecutionStream
						var errMsg *interfaces.ErrorMessage
						if internal {
							response, errMsg = handler.ExecuteModelStream(callCtx, request)
						} else {
							response, errMsg = handler.ExecuteProtocolStreamWithAuthManager(callCtx, protocol)
						}
						if errMsg != nil {
							return errMsg
						}
						var body strings.Builder
						for chunk := range response.Chunks {
							if chunk.Err != nil {
								return &interfaces.ErrorMessage{StatusCode: 500, Error: chunk.Err}
							}
							body.Write(chunk.Payload)
						}
						if !strings.Contains(body.String(), "fixture") {
							t.Errorf("missing image stream payload: %q", body.String())
						}
						return nil
					}
					var response ModelExecutionResponse
					var errMsg *interfaces.ErrorMessage
					if internal {
						response, errMsg = handler.ExecuteModel(callCtx, request)
					} else {
						response, errMsg = handler.ExecuteProtocolWithAuthManager(callCtx, protocol)
					}
					if errMsg == nil && !strings.Contains(string(response.Body), "fixture") {
						t.Errorf("missing image response payload: %q", response.Body)
					}
					return errMsg
				}
				done := make(chan *interfaces.ErrorMessage, 32)
				total := 0
				for index, limit := range limits {
					for slot := 0; slot < limit; slot++ {
						total++
						go func() { done <- execute(index, slot%3, "") }()
					}
				}
				for count := 0; count < total; count++ {
					select {
					case <-arrived:
					case errMsg := <-done:
						t.Fatalf("execution returned before the upstream gate: %+v", errMsg)
					case <-ctx.Done():
						t.Fatal("image executions did not reach their credential transports")
					}
				}
				for index, limit := range limits {
					if active, actualLimit := manager.CredentialConcurrencySnapshot(ids[index]); active != limit || actualLimit != limit {
						t.Fatalf("credential %d concurrency=%d/%d, want %d/%d", index, active, actualLimit, limit, limit)
					}
					if errMsg := execute(index, 2, ""); errMsg == nil || errMsg.StatusCode != http.StatusTooManyRequests || !coreauth.IsCredentialConcurrencyError(errMsg.Error) {
						t.Fatalf("full credential %d returned %+v, want local 429", index, errMsg)
					}
				}
				unblock()
				for count := 0; count < total; count++ {
					select {
					case errMsg := <-done:
						if errMsg != nil {
							t.Fatalf("image execution failed: %+v", errMsg)
						}
					case <-ctx.Done():
						t.Fatal("image executions did not complete")
					}
				}
				for stage := int32(1); stage <= 2; stage++ {
					rejectStage.Store(stage)
					if errMsg := execute(0, 0, ""); errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
						t.Fatalf("interceptor stage %d returned %+v, want 403", stage, errMsg)
					}
				}
				rejectStage.Store(0)
				conflicts := 0
				if internal {
					for index := range limits {
						if errMsg := execute(index, 0, globalProxy.URL); errMsg == nil || errMsg.StatusCode != http.StatusBadRequest {
							t.Fatalf("credential %d proxy conflict returned %+v, want 400", index, errMsg)
						}
						conflicts++
					}
				}
				for index, limit := range limits {
					if active, actualLimit := manager.CredentialConcurrencySnapshot(ids[index]); active != 0 || actualLimit != limit {
						t.Errorf("credential %d leaked concurrency=%d/%d", index, active, actualLimit)
					}
					if count := networkCounts[index].Load(); count != int32(limit) {
						t.Errorf("credential %d network count=%d, want %d", index, count, limit)
					}
					current, _ := manager.GetByID(ids[index])
					if current.ProxyURL != proxyURLs[index] || current.Failed != 0 || current.Unavailable || current.Quota.Exceeded {
						t.Errorf("local rejection changed credential %d policy/health", index)
					}
				}
				if count := globalCount.Load(); count != 0 {
					t.Errorf("global proxy received %d requests", count)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(completions) != total+3+2+conflicts || len(beforePaths) != len(completions) || len(afterPaths) != total+1 || len(keyCounts) != 3 {
					t.Errorf("lifecycle mismatch: completion=%d before=%d after=%d keys=%d", len(completions), len(beforePaths), len(afterPaths), len(keyCounts))
				}
				outcomes := make(map[pluginapi.RequestCompletionOutcome]int)
				for id, completion := range completions {
					outcomes[completion.Outcome]++
					if beforePaths[id][coreexecutor.RequestPathMetadataKey] != "/v1/images/generations" {
						t.Errorf("path override mutated before-auth metadata for %s", id)
					}
					if after := afterPaths[id]; after != nil && after[coreexecutor.RequestPathMetadataKey] != "/v1/images/edits" {
						t.Errorf("path override mutated after-auth metadata for %s", id)
					}
				}
				if outcomes[pluginapi.RequestCompletionSucceeded] != total || outcomes[pluginapi.RequestCompletionRejected] != 2 || outcomes[pluginapi.RequestCompletionFailed] != 3+conflicts {
					t.Errorf("terminal outcomes=%v", outcomes)
				}
			})
		}
	}
}
