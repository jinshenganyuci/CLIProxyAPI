package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestNestedModelExecutionDoesNotChangeOuterProviderCallback(t *testing.T) {
	for _, outerProvider := range []string{"codex", "callback-helper"} {
		for _, nestedStream := range []bool{false, true} {
			t.Run(fmt.Sprintf("outer=%s/nested_stream=%t", outerProvider, nestedStream), func(t *testing.T) {
				innerProvider := "codex"
				if outerProvider == innerProvider {
					innerProvider = "callback-helper"
				}
				outerModel, innerModel := "outer-"+t.Name(), "inner-"+t.Name()
				outerAuthID, innerAuthID := "auth-"+outerModel, "auth-"+innerModel
				manager := coreauth.NewManager(nil, nil, nil)
				handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
				completion := []byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
				streamResult := func() *coreexecutor.StreamResult {
					chunks := make(chan coreexecutor.StreamChunk, 1)
					chunks <- coreexecutor.StreamChunk{Payload: completion}
					close(chunks)
					return &coreexecutor.StreamResult{Chunks: chunks}
				}
				innerExecutor := &modelExecutionCaptureExecutor{
					provider: innerProvider,
					execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
						return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
					},
					stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
						return streamResult(), nil
					},
				}
				outerExecutor := &modelExecutionCaptureExecutor{
					provider: outerProvider,
					stream: func(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
						request := ModelExecutionRequest{EntryProtocol: "openai-response", ExitProtocol: "openai-response", Model: innerModel, Stream: nestedStream, Body: []byte(fmt.Sprintf(`{"model":%q,"stream":%t}`, innerModel, nestedStream))}
						if nestedStream {
							stream, errMsg := handler.ExecuteModelStream(ctx, request)
							if errMsg != nil {
								return nil, fmt.Errorf("nested stream: %v", errMsg.Error)
							}
							for chunk := range stream.Chunks {
								if chunk.Err != nil {
									return nil, chunk.Err
								}
							}
						} else if _, errMsg := handler.ExecuteModel(ctx, request); errMsg != nil {
							return nil, fmt.Errorf("nested execute: %v", errMsg.Error)
						}
						return streamResult(), nil
					},
				}
				for _, entry := range []struct {
					executor *modelExecutionCaptureExecutor
					model    string
					authID   string
				}{{outerExecutor, outerModel, outerAuthID}, {innerExecutor, innerModel, innerAuthID}} {
					manager.RegisterExecutor(entry.executor)
					if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: entry.authID, Provider: entry.executor.Identifier(), Status: coreauth.StatusActive}); errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(entry.authID, entry.executor.Identifier(), []*registry.ModelInfo{{ID: entry.model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(entry.authID) })
				}

				ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				logging.SetGinRequestID(ginCtx, "scope123")
				ctx := context.WithValue(context.Background(), "gin", ginCtx)
				var preserveNativeOutput atomic.Bool
				var callbacksMu sync.Mutex
				var providers, authIDs []string
				ctx = WithSelectedAuthProviderCallback(ctx, func(provider string) {
					preserveNativeOutput.Store(provider == "codex")
					callbacksMu.Lock()
					providers = append(providers, provider)
					callbacksMu.Unlock()
				})
				ctx = WithSelectedAuthIDCallback(ctx, func(authID string) {
					callbacksMu.Lock()
					authIDs = append(authIDs, authID)
					callbacksMu.Unlock()
				})
				data, _, errors := handler.ExecuteStreamWithAuthManager(ctx, "openai-response", outerModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, outerModel)), "")
				for range data {
				}
				for errMsg := range errors {
					if errMsg != nil {
						t.Fatalf("outer stream: %v", errMsg.Error)
					}
				}
				callbacksMu.Lock()
				gotProviders := append([]string(nil), providers...)
				gotAuthIDs := append([]string(nil), authIDs...)
				callbacksMu.Unlock()
				if preserveNativeOutput.Load() != (outerProvider == "codex") || !reflect.DeepEqual(gotProviders, []string{outerProvider}) {
					t.Fatalf("nested %s selection changed outer %s fidelity: preserve=%t providers=%v", innerProvider, outerProvider, preserveNativeOutput.Load(), gotProviders)
				}
				if !reflect.DeepEqual(gotAuthIDs, []string{outerAuthID, innerAuthID}) {
					t.Fatalf("existing auth ID callback changed: %v", gotAuthIDs)
				}
				innerAuth, _ := manager.GetByID(innerAuthID)
				if traceID := logging.GetGinCPATraceID(ginCtx); !strings.Contains(traceID, innerAuth.Index) {
					t.Fatalf("existing auth index callback changed: trace=%q index=%q", traceID, innerAuth.Index)
				}

				if _, _, errMsg := handler.ExecuteWithAuthManager(ctx, "openai-response", innerModel, []byte(fmt.Sprintf(`{"model":%q}`, innerModel)), ""); errMsg != nil {
					t.Fatalf("subsequent outer selection: %v", errMsg.Error)
				}
				callbacksMu.Lock()
				gotProviders = append([]string(nil), providers...)
				callbacksMu.Unlock()
				if !reflect.DeepEqual(gotProviders, []string{outerProvider, innerProvider}) || preserveNativeOutput.Load() != (innerProvider == "codex") {
					t.Fatalf("parent callback stopped observing outer selections: %v", gotProviders)
				}
			})
		}
	}
}
