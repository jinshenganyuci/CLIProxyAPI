package executor

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestV855OpenAICompatCompactionKeepsFinalPayloadProxyLeaseAndUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, outcome := range []string{"complete", "incomplete", "empty"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, outcome), func(t *testing.T) {
				const model = "compaction855-model"
				var selectedHits, fallbackHits atomic.Int32
				var manager *coreauth.Manager
				bodies := make(chan []byte, 2)
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					selectedHits.Add(1)
					if r.URL.Host != "compaction855.invalid" || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic855" {
						t.Errorf("credential route changed: %s", r.URL)
					}
					if active, _ := manager.CredentialConcurrencySnapshot(t.Name()); active != 1 {
						t.Errorf("live slots = %d, want 1", active)
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
					}
					bodies <- body
					if ojson.GetBytes(body, "stream").Bool() || ojson.GetBytes(body, "tools").Exists() || strings.Contains(string(body), "compaction_trigger") {
						t.Errorf("summary preparation did not precede final payload: %s", body)
					}
					if ojson.GetBytes(body, "audit.calls.#").Int() != 1 || ojson.GetBytes(body, "audit.large").Raw != "9007199254740993" || ojson.GetBytes(body, "max_tokens").Int() != 257 {
						t.Errorf("final payload was repeated or changed: %s", body)
					}
					finish, content := "stop", "Summary retaining COBALT-855."
					if outcome == "incomplete" {
						finish = "length"
					} else if outcome == "empty" {
						content = ""
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"id":"chat855","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":%q}],"usage":{"prompt_tokens":12,"completion_tokens":6,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":2}}}`, model, content, finish)
				}))
				defer selected.Close()
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fallbackHits.Add(1)
					w.WriteHeader(http.StatusBadGateway)
				}))
				defer fallback.Close()
				match := []config.PayloadModelRule{{Name: model, Protocol: "openai"}}
				cfg := &config.Config{AuthDir: t.TempDir(), SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}, Payload: config.PayloadConfig{
					Override:    []config.PayloadRule{{Models: match, Params: map[string]any{"audit.calls.-1": "once", "max_tokens": 257}}},
					OverrideRaw: []config.PayloadRule{{Models: match, Params: map[string]any{"audit.large": "9007199254740993"}}},
					Filter:      []config.PayloadFilterRule{{Models: match, Params: []string{"tool_choice"}}},
				}}
				manager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				exec := NewOpenAICompatExecutor("openai-compatibility", cfg)
				manager.RegisterExecutor(exec)
				credential, errRegister := manager.Register(t.Context(), &coreauth.Auth{ID: t.Name(), Provider: exec.Identifier(), Status: coreauth.StatusActive, ProxyURL: selected.URL, Attributes: map[string]string{"base_url": "http://compaction855.invalid", "api_key": "synthetic855"}})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
				defer registry.GetGlobalRegistry().UnregisterClient(credential.ID)
				capture := &providerV810UsageCapture{authID: credential.ID, records: make(chan usage.Record, 8)}
				usage.RegisterNamedPlugin(t.Name(), capture)
				defer usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{})
				api := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
				router := gin.New()
				router.POST("/v1/responses", api.Responses)
				payload := fmt.Sprintf(`{"model":%q,"stream":%t,"input":[{"type":"message","role":"user","content":"Remember COBALT-855"},{"type":"compaction_trigger"}],"tools":[{"type":"function","name":"tool","parameters":{"type":"object"}}]}`, model, stream)
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if outcome != "complete" {
					if response.Code != http.StatusBadGateway {
						t.Errorf("failed summary status = %d, body = %s", response.Code, response.Body.String())
					}
				} else {
					if response.Code != http.StatusOK {
						t.Fatalf("summary status = %d, body = %s", response.Code, response.Body.String())
					}
					compaction := ojson.ParseBytes(response.Body.Bytes())
					if stream {
						compaction = claudeCompactionCompletedResponse(t, response.Body.String()).Get("response")
					}
					if items := compaction.Get("output").Array(); len(items) != 1 || items[0].Get("type").String() != "compaction" {
						t.Fatalf("invalid compaction response: %s", response.Body.String())
					}
					if summary, errOpen := helps.UnsealAntigravityCompaction(compaction.Get("output.0.encrypted_content").String()); errOpen != nil || summary != "Summary retaining COBALT-855." {
						t.Errorf("capsule summary = %q, err = %v", summary, errOpen)
					}
				}
				records := providerV810UsageRecords(t, capture)
				if len(records) != 1 {
					t.Fatalf("usage records = %d, want 1", len(records))
				}
				record := records[0]
				if record.Failed != (outcome != "complete") || record.Detail.InputTokens != 12 || record.Detail.OutputTokens != 6 || record.Detail.CachedTokens != 4 || record.Detail.ReasoningTokens != 2 {
					t.Errorf("summary outcome accounting changed: failed=%t detail=%+v", record.Failed, record.Detail)
				}
				if active, _ := manager.CredentialConcurrencySnapshot(credential.ID); active != 0 {
					t.Errorf("leaked slots = %d", active)
				}
				if selectedHits.Load() != 1 || fallbackHits.Load() != 0 {
					t.Errorf("selected/fallback hits = %d/%d, want 1/0", selectedHits.Load(), fallbackHits.Load())
				}
			})
		}
	}
}
