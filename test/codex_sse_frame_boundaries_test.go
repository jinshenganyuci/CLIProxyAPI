package test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestCodexHTTPStreamPreservesSSEBoundariesBeforeTerminalError(t *testing.T) {
	for _, scenario := range []struct {
		name, frame string
	}{
		{"data_only", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic-boundary-delta\"}\n\n"},
		{"data_before_event", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic-boundary-delta\"}\nevent: response.output_text.delta\n\n"},
		{"multiple_data_lines", "data: {\"type\":\"response.output_text.delta\",\ndata: \"delta\":\"synthetic-boundary-delta\"}\n\n"},
	} {
		for _, native := range []bool{false, true} {
			for _, buffering := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/native=%t/buffering=%t", scenario.name, native, buffering), func(t *testing.T) {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, _ = io.Copy(io.Discard, r.Body)
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"boundary-response\",\"output\":[]}}\n\n")
						_, _ = io.WriteString(w, scenario.frame)
						_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"fixture_terminal_failure\",\"message\":\"Synthetic public stream failure\"}}\n\n")
					}))
					defer upstream.Close()
					cfg := &config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: buffering}}
					manager := coreauth.NewManager(nil, nil, nil)
					manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
					credential := &coreauth.Auth{ID: t.Name(), Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": upstream.URL}}
					if _, errRegister := manager.Register(coreauth.WithSkipPersist(t.Context()), credential); errRegister != nil {
						t.Fatal(errRegister)
					}
					const model = "sse-boundary-model"
					registry.GetGlobalRegistry().RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
					handler := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
					router := gin.New()
					router.POST("/v1/responses", handler.Responses)
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"sse-boundary-model","stream":true,"input":[]}`))
					request.Header.Set("Content-Type", "application/json")
					if native {
						request.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
						request.Header.Set("User-Agent", "codex_cli_rs/0.154.0")
					}
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, request)
					body := recorder.Body.String()
					if recorder.Code != http.StatusOK {
						t.Fatalf("stream failed before its initial event: status=%d body=%q", recorder.Code, body)
					}
					var deltaSeen, failureSeen bool
					for _, frame := range strings.Split(body, "\n\n") {
						var data []string
						for _, line := range strings.Split(frame, "\n") {
							if strings.HasPrefix(line, "data:") {
								data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
							}
						}
						payload := strings.Join(data, "\n")
						switch gjson.Get(payload, "type").String() {
						case "response.output_text.delta":
							deltaSeen = gjson.Get(payload, "delta").String() == "synthetic-boundary-delta"
						case "error", "response.failed":
							if failureSeen || !deltaSeen || !strings.Contains(payload, "Synthetic public stream failure") || !strings.Contains(payload, "fixture_terminal_failure") || strings.Contains(payload, "synthetic-boundary-delta") {
								t.Fatalf("preceding SSE frame replaced the real terminal error: %q", body)
							}
							failureSeen = true
						case "response.completed":
							t.Fatalf("terminal error became successful completion: %q", body)
						}
					}
					if !deltaSeen || !failureSeen {
						t.Fatalf("delta or precise failure lost: %q", body)
					}
				})
			}
		}
	}
}
