package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestInternalPinnedModelExecutionPreservesCredentialAndOuterObserver(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, pin := range []string{"selected", "missing", "disabled", "other-provider"} {
			t.Run(fmt.Sprintf("stream=%t/pin=%s", stream, pin), func(t *testing.T) {
				model := "pinned-identity-" + t.Name()
				selectedID := model + "-selected"
				const selectedProxy = "socks5://selected.example.test:1080"
				const selectedNamespace = "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"
				var mu sync.Mutex
				var calls, providerCallbacks int
				var selectedCallbacks []string
				capture := func(auth *coreauth.Auth) {
					mu.Lock()
					defer mu.Unlock()
					calls++
					if auth.ID != selectedID || auth.ProxyURL != selectedProxy || auth.Metadata["codex_identity_namespace"] != selectedNamespace {
						t.Errorf("executor received a different credential or lost its proxy/identity: id=%s", auth.ID)
					}
				}
				executor := &modelExecutionCaptureExecutor{
					provider: "codex",
					execute: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
						capture(auth)
						return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
					},
					stream: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
						capture(auth)
						chunks := make(chan coreexecutor.StreamChunk, 1)
						chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
						close(chunks)
						return &coreexecutor.StreamResult{Chunks: chunks}, nil
					},
				}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.RegisterExecutor(executor)
				for _, auth := range []*coreauth.Auth{
					{ID: selectedID, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: selectedProxy, Attributes: map[string]string{"priority": "0"}, Metadata: map[string]any{"codex_identity_namespace": selectedNamespace}},
					{ID: model + "-higher-priority", Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"priority": "100"}},
					{ID: model + "-disabled", Provider: "codex", Status: coreauth.StatusDisabled, Disabled: true},
					{ID: model + "-other-provider", Provider: "other-provider", Status: coreauth.StatusActive},
				} {
					if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				}
				handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
				ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				logging.SetGinRequestID(ginCtx, "pin12345")
				ctx := context.WithValue(context.Background(), "gin", ginCtx)
				ctx = WithSelectedAuthProviderCallback(ctx, func(string) {
					mu.Lock()
					providerCallbacks++
					mu.Unlock()
				})
				ctx = WithSelectedAuthIDCallback(ctx, func(id string) {
					mu.Lock()
					selectedCallbacks = append(selectedCallbacks, id)
					mu.Unlock()
				})
				request := ModelExecutionRequest{EntryProtocol: "openai-response", ExitProtocol: "openai-response", Model: model, Stream: stream, ForcedProvider: "codex", AuthID: model + "-" + pin, Body: []byte(fmt.Sprintf(`{"model":%q,"stream":%t}`, model, stream))}
				var failed bool
				if stream {
					result, errMsg := handler.ExecuteModelStream(ctx, request)
					failed = errMsg != nil
					if errMsg == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								failed = true
							}
						}
					}
				} else {
					_, errMsg := handler.ExecuteModel(ctx, request)
					failed = errMsg != nil
				}
				mu.Lock()
				defer mu.Unlock()
				if failed != (pin != "selected") {
					t.Fatalf("execution failed=%t for pin=%s", failed, pin)
				}
				if providerCallbacks != 0 || selectedAuthProviderCallbackFromContext(ctx) == nil {
					t.Fatalf("nested call changed the outer provider observer: calls=%d", providerCallbacks)
				}
				if pin != "selected" {
					if calls != 0 || len(selectedCallbacks) != 0 {
						t.Fatalf("invalid credential pin fell back: calls=%d selected=%v", calls, selectedCallbacks)
					}
					return
				}
				if calls != 1 || len(selectedCallbacks) != 1 || selectedCallbacks[0] != selectedID {
					t.Fatalf("explicit credential or existing ID callback changed: calls=%d selected=%v", calls, selectedCallbacks)
				}
				selected, _ := manager.GetByID(selectedID)
				if traceID := logging.GetGinCPATraceID(ginCtx); !strings.Contains(traceID, selected.Index) {
					t.Fatalf("existing auth index callback changed: trace=%q index=%q", traceID, selected.Index)
				}
			})
		}
	}
}
