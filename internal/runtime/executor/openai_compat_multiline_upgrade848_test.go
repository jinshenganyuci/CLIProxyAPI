package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// The real Chat handler frames each translated JSON chunk once. Legal multiline
// upstream data fields must remain one complete JSON event after that boundary.
func TestV848OpenAICompatMultilineSSESurvivesChatWriter(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		for _, multiline := range []bool{false, true} {
			t.Run(fmt.Sprintf("crlf=%t/multiline=%t", newline == "\r\n", multiline), func(t *testing.T) {
				const model = "multiline848-model"
				content := `{"id":"chat848","object":"chat.completion.chunk","exact_integer":9007199254740993,"choices":[{"index":0,"delta":{"role":"assistant","content":"literal\nline <tag>","tool_calls":[{"index":0,"id":"call848","type":"function","function":{"name":"weather","arguments":"{}"}}]},"finish_reason":null}]}`
				finished := `{"id":"chat848","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
				measured := `{"id":"chat848","object":"chat.completion.chunk","choices":[],"service_tier":"flex","usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}}`
				frame := func(raw, split string) string {
					if multiline {
						raw = strings.Replace(raw, split, split+newline+"data: ", 1)
					}
					return "data: " + raw + newline + newline
				}
				wire := frame(content, `"choices":`) + "data: " + finished + newline + newline + frame(measured, `"usage":`) + "data: [DONE]" + newline + newline
				var selectedHits, fallbackHits atomic.Int32
				var manager *auth.Manager
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					selectedHits.Add(1)
					if r.URL.Host != "multiline848.invalid" || r.Header.Get("Authorization") != "Bearer synthetic848" {
						t.Errorf("selected credential route changed: %s", r.URL)
					}
					if active, _ := manager.CredentialConcurrencySnapshot(t.Name()); active != 1 {
						t.Errorf("live slots=%d", active)
					}
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, wire)
				}))
				defer selected.Close()
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fallbackHits.Add(1); w.WriteHeader(502) }))
				defer fallback.Close()
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}}
				manager = auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				executor := NewOpenAICompatExecutor("openai-compatibility", cfg)
				manager.RegisterExecutor(executor)
				credential, errRegister := manager.Register(t.Context(), &auth.Auth{ID: t.Name(), Provider: executor.Identifier(), Status: auth.StatusActive, ProxyURL: selected.URL, Attributes: map[string]string{"base_url": "http://multiline848.invalid", "api_key": "synthetic848"}})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
				defer registry.GetGlobalRegistry().UnregisterClient(credential.ID)
				capture := &providerV810UsageCapture{authID: credential.ID, records: make(chan usage.Record, 8)}
				usage.RegisterNamedPlugin(t.Name(), capture)
				defer usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{})
				api := openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
				router := gin.New()
				router.POST("/v1/chat/completions", api.ChatCompletions)
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"multiline848-model","messages":[{"role":"user","content":"synthetic"}],"stream":true,"stream_options":{"include_usage":true}}`))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != 200 {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.Bytes())
				}
				var received [][]byte
				done := 0
				// Standard SSE parsing only concatenates data fields. Unknown raw JSON lines
				// are not data; this catches an invalid one-prefix multiline re-emission.
				for _, frame := range strings.Split(strings.ReplaceAll(response.Body.String(), "\r\n", "\n"), "\n\n") {
					var data []string
					for _, line := range strings.Split(frame, "\n") {
						if strings.HasPrefix(line, "data:") {
							data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
						}
					}
					payload := []byte(strings.Join(data, "\n"))
					if len(payload) == 0 {
						continue
					}
					if string(payload) == "[DONE]" {
						done++
						continue
					}
					if !json.Valid(payload) {
						t.Errorf("standard SSE reader received truncated JSON: %s; wire=%q", payload, response.Body.String())
						continue
					}
					var compact bytes.Buffer
					if errCompact := json.Compact(&compact, payload); errCompact != nil {
						t.Fatal(errCompact)
					}
					received = append(received, bytes.Clone(compact.Bytes()))
				}
				if done != 1 || len(received) != 3 {
					t.Errorf("events=%d DONE=%d, want 3/1", len(received), done)
				} else {
					for i, want := range []string{content, finished, measured} {
						if string(received[i]) != want {
							t.Errorf("event %d changed: %s", i, received[i])
						}
					}
					if gjson.GetBytes(received[0], "exact_integer").Raw != "9007199254740993" || gjson.GetBytes(received[0], "choices.0.delta.content").String() != "literal\nline <tag>" {
						t.Error("JSON compaction changed scalar data")
					}
				}
				records := providerV810UsageRecords(t, capture)
				if len(records) != 1 {
					t.Fatalf("usage records=%d", len(records))
				}
				r := records[0]
				tools := r.Detail.ToolUsageDetails
				if r.Failed || r.Detail.InputTokens != 12 || r.Detail.OutputTokens != 8 || r.Detail.CachedTokens != 3 || r.Detail.ReasoningTokens != 2 || r.Detail.UpstreamServiceTier != "flex" || tools == nil || tools.ToolCallsRequested == nil || *tools.ToolCallsRequested != 1 {
					t.Errorf("structured accounting changed: %+v", r)
				}
				if active, _ := manager.CredentialConcurrencySnapshot(credential.ID); active != 0 {
					t.Errorf("leaked slots=%d", active)
				}
				if selectedHits.Load() != 1 || fallbackHits.Load() != 0 {
					t.Errorf("selected=%d fallback=%d", selectedHits.Load(), fallbackHits.Load())
				}
			})
		}
	}
}
