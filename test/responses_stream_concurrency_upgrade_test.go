package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResponsesStreamErrorUpgradePreservesAdmission(t *testing.T) {
	for _, mode := range []string{"incomplete", "cancel", "busy"} {
		t.Run(mode, func(t *testing.T) {
			const model = "gpt-5.4"
			var requests atomic.Int32
			entered := make(chan struct{})
			upstreamCanceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_admission\"}}\n\n")
				w.(http.Flusher).Flush()
				if mode == "cancel" {
					close(entered)
					<-r.Context().Done()
					close(upstreamCanceled)
				}
			}))
			defer server.Close()
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			cfg := &config.Config{}
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			authID := "response-admission-upgrade-" + uuid.NewString()
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			auth, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
				ID: authID, Provider: "codex", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic-admission-key"},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			if mode == "busy" {
				for range 10 {
					release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), auth)
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					defer release()
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"synthetic","stream":true}`, model))).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			done := make(chan struct{})
			go func() {
				defer close(done)
				openaihandlers.NewOpenAIResponsesAPIHandler(base).Responses(c)
			}()
			await := func(signal <-chan struct{}) {
				t.Helper()
				select {
				case <-signal:
				case <-time.After(10 * time.Second):
					t.Fatal("timed out waiting for stream test synchronization")
				}
			}
			if mode == "cancel" {
				await(entered)
				if used, limit := manager.CredentialConcurrencySnapshot(authID); used != 1 || limit != 10 {
					t.Fatalf("active stream admission = %d/%d, want 1/10", used, limit)
				}
				cancel()
				await(upstreamCanceled)
			}
			await(done)
			wantUsed := 0
			if mode == "busy" {
				wantUsed = 10
			}
			if used, limit := manager.CredentialConcurrencySnapshot(authID); used != wantUsed || limit != 10 {
				t.Fatalf("final admission = %d/%d, want %d/10", used, limit, wantUsed)
			}
			switch mode {
			case "incomplete":
				detail := terminalStreamErrorDetail(t, recorder.Body.String())
				if detail["code"] != "request_timeout" || detail["type"] != "server_error" || requests.Load() != 1 {
					t.Fatalf("incomplete error/attempts = %+v/%d", detail, requests.Load())
				}
			case "busy", "cancel":
				state, _ := manager.GetByID(authID)
				if state.Failed != 0 || state.LastError != nil || state.Unavailable || state.Quota.Exceeded || len(state.ModelStates) != 0 {
					t.Fatalf("local refusal/cancellation mutated upstream state: %+v", state)
				}
				if mode == "busy" && (recorder.Code != http.StatusTooManyRequests || requests.Load() != 0) {
					t.Fatalf("busy status/outbound = %d/%d, want 429/0", recorder.Code, requests.Load())
				}
				if mode == "cancel" && strings.Contains(recorder.Body.String(), "request_timeout") {
					t.Fatalf("client cancellation became upstream timeout: %s", recorder.Body.String())
				}
			}
		})
	}
}
