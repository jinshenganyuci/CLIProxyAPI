package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestResponsesSSEPrivateFilteringPreservesTerminalErrors(t *testing.T) {
	for _, codexClient := range []bool{false, true} {
		for _, scenario := range []struct {
			name, frame, standardTerminalEvent string
		}{
			{"private_event_public_failure", "event: responsesapi.websocket_timing\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"provider busy\"}}}\n\n", "response.failed"},
			{"private_event_error_object", "event: codex.rate_limits\ndata: {\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"provider busy\"}}\n\n", "error"},
			{"public_error_private_type", "event: error\ndata: {\"type\":\"responsesapi.websocket_timing\",\"message\":\"provider busy\"}\n\n", "error"},
			{"data_only_private_type_error", "data: {\"type\":\"codex.rate_limits\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"provider busy\"}}\n\n", "error"},
		} {
			t.Run(fmt.Sprintf("codex=%t/%s", codexClient, scenario.name), func(t *testing.T) {
				var output bytes.Buffer
				failureEvent := "error"
				if codexClient {
					failureEvent = "response.failed"
				}
				framer := &responsesSSEFramer{failureEvent: failureEvent, isCodexClient: codexClient}
				expectedTerminalEvent := scenario.standardTerminalEvent
				if codexClient {
					expectedTerminalEvent = "response.failed"
				}
				// Exercise frame reassembly rather than invoking the filter directly.
				split := len(scenario.frame) / 2
				framer.WriteChunk(&output, []byte(scenario.frame[:split]))
				framer.WriteChunk(&output, []byte(scenario.frame[split:]))
				framer.WriteChunk(&output, []byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))
				framer.Flush(&output)
				if framer.terminalError == nil || framer.terminalEvent != expectedTerminalEvent || framer.dataFrames != 1 {
					t.Fatalf("private filtering lost terminal error: event=%q error=%v frames=%d output=%q", framer.terminalEvent, framer.terminalError, framer.dataFrames, output.String())
				}
				if !strings.Contains(output.String(), "provider busy") || strings.Contains(output.String(), "event: responsesapi.") || strings.Contains(output.String(), "event: codex.") || strings.Contains(output.String(), "response.completed") {
					t.Fatalf("terminal failure was replaced by private telemetry or completion: %q", output.String())
				}
			})
		}
	}
}

type privateTelemetryFailureExecutor struct {
	initialFailureStreamExecutor
}

func (*privateTelemetryFailureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 3)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: codex.rate_limits\ndata: {\"type\":\"codex.rate_limits\",\"internal\":\"private-canary\"}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: responsesapi.websocket_timing\ndata: {\"type\":\"responsesapi.websocket_timing\",\"internal\":\"private-canary\"}\n\n")}
	chunks <- coreexecutor.StreamChunk{Err: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Code: "rate_limit_exceeded", Message: "upstream capacity exhausted"}}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestResponsesPrivateTelemetryDoesNotCommitSuccessfulSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, userAgent := range []string{"ordinary-responses-client", "codex_cli_rs/0.154.0"} {
		t.Run(userAgent, func(t *testing.T) {
			executor := &privateTelemetryFailureExecutor{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			credential := &coreauth.Auth{ID: t.Name(), Provider: executor.Identifier(), Status: coreauth.StatusActive}
			if _, errRegister := manager.Register(t.Context(), credential); errRegister != nil {
				t.Fatal(errRegister)
			}
			const model = "private-telemetry-upgrade-model"
			registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
			handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			router.POST("/v1/responses", handler.Responses)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"private-telemetry-upgrade-model","input":"hello","stream":true}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", userAgent)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			body := recorder.Body.String()
			if recorder.Code != http.StatusTooManyRequests || !strings.Contains(body, "upstream capacity exhausted") {
				t.Fatalf("private telemetry committed success or lost the real error: status=%d body=%q", recorder.Code, body)
			}
			if strings.Contains(body, "private-canary") || strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
				t.Fatalf("private-only stream was exposed: headers=%v body=%q", recorder.Header(), body)
			}
		})
	}
}

func TestResponsesPrivateFilteringRetainsSelectedNativeCodexMetadata(t *testing.T) {
	const metadata = `{"type":"codex.response.metadata","headers":{"x-codex-turn-state":"opaque-next-state"},"future":{"nested":true}}`
	for _, selectedCodex := range []bool{false, true} {
		t.Run(fmt.Sprintf("selected_codex=%t", selectedCodex), func(t *testing.T) {
			handler, recorder, ginContext, flusher := newResponsesStreamTestHandler(t)
			// A native marker alone must not preserve private events from a different
			// provider. The selection callback is the authoritative additional signal.
			ginContext.Request.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
			framer := &responsesSSEFramer{preserveCompletionOutput: func() bool { return selectedCodex }}
			data := make(chan []byte, 4)
			data <- []byte("data: " + metadata + "\n\n")
			data <- []byte("data: {\"type\":\"codex.rate_limits\",\"private\":\"rate-canary\"}\n\n")
			data <- []byte("data: {\"type\":\"responsesapi.websocket_timing\",\"private\":\"timing-canary\"}\n\n")
			data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n")
			close(data)
			errs := make(chan *interfaces.ErrorMessage)
			close(errs)
			handler.forwardResponsesStream(ginContext, flusher, func(error) {}, data, errs, framer)
			body := recorder.Body.String()
			if strings.Contains(body, metadata) != selectedCodex {
				t.Fatalf("selected native metadata mismatch: selected_codex=%t body=%q", selectedCodex, body)
			}
			if strings.Contains(body, "rate-canary") || strings.Contains(body, "timing-canary") || !strings.Contains(body, "response.completed") {
				t.Fatalf("metadata compatibility changed telemetry filtering or completion: %q", body)
			}
		})
	}
}
