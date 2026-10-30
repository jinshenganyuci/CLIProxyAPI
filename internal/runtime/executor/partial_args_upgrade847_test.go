package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV847VertexPartialArgsTerminalUsageAndLease(t *testing.T) {
	for _, format := range []translator.Format{translator.FormatOpenAI, translator.FormatOpenAIResponse, translator.FormatClaude} {
		for _, mode := range []string{"incomplete", "complete", "cancel"} {
			t.Run(fmt.Sprintf("%s/%s", format, mode), func(t *testing.T) {
				complete := mode == "complete"
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				const model = "gemini-3.1-pro-preview"
				var selectedHits, fallbackHits atomic.Int32
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					selectedHits.Add(1)
					if !r.URL.IsAbs() || r.URL.Host != "partial847.invalid" {
						t.Errorf("wrong proxy target: %s", r.URL)
					}
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, `data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":4,"totalTokenCount":16},"responseId":"partial847","modelVersion":"gemini-3.1-pro-preview"}`+"\n\n")
					if mode == "cancel" {
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					content := ""
					if complete {
						content = `,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"York"}],"willContinue":false}}]}`
					}
					_, _ = fmt.Fprintf(w, "data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"%s}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":4,\"totalTokenCount\":16},\"responseId\":\"partial847\",\"modelVersion\":\"gemini-3.1-pro-preview\"}\n\n", content)
				}))
				defer func() { cancel(); selected.Close() }()
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fallbackHits.Add(1); w.WriteHeader(502) }))
				defer fallback.Close()
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewGeminiVertexExecutor(cfg))
				credential, errRegister := manager.Register(t.Context(), &coreauth.Auth{ID: t.Name(), Provider: "vertex", Status: coreauth.StatusActive, ProxyURL: selected.URL, Attributes: map[string]string{"api_key": "synthetic847", "base_url": "http://partial847.invalid"}})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(credential.ID, "vertex", []*registry.ModelInfo{{ID: model}})
				defer registry.GetGlobalRegistry().UnregisterClient(credential.ID)
				capture := &providerV810UsageCapture{authID: credential.ID, records: make(chan usage.Record, 4)}
				usage.RegisterNamedPlugin(t.Name(), capture)
				defer usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{})
				request := []byte(`{"contents":[{"role":"user","parts":[{"text":"weather"}]}],"tools":[{"functionDeclarations":[{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}]}`)
				result, errExecute := manager.ExecuteStream(ctx, []string{"vertex"}, core.Request{Model: model, Payload: request}, core.Options{SourceFormat: translator.FormatGemini, ResponseFormat: format, Stream: true})
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				var output, arguments bytes.Buffer
				var streamErr error
				for chunk := range result.Chunks {
					output.Write(chunk.Payload)
					for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
						data := bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(line), []byte("data:")))
						switch format {
						case translator.FormatOpenAI:
							arguments.WriteString(gjson.GetBytes(data, "choices.0.delta.tool_calls.0.function.arguments").String())
						case translator.FormatClaude:
							arguments.WriteString(gjson.GetBytes(data, "delta.partial_json").String())
						case translator.FormatOpenAIResponse:
							if gjson.GetBytes(data, "type").String() == "response.function_call_arguments.delta" {
								arguments.WriteString(gjson.GetBytes(data, "delta").String())
							}
						}
					}
					if chunk.Err != nil {
						streamErr = chunk.Err
					}
					if mode == "cancel" {
						cancel()
					}
				}
				if mode == "cancel" && streamErr != nil && !errors.Is(streamErr, context.Canceled) {
					t.Errorf("cancellation was overwritten: %v", streamErr)
				}
				if mode != "cancel" && (streamErr == nil) != complete {
					t.Errorf("incomplete argument stream error=%v complete=%t output=%s", streamErr, complete, output.Bytes())
				}
				if complete && gjson.GetBytes(arguments.Bytes(), "city").String() != "New York" {
					t.Errorf("completed arguments missing: %s", output.Bytes())
				}
				records := antigravityUsageRecordsAfterDelivery815(t, capture)
				if len(records) != 1 || records[0].Failed == complete || records[0].Detail.InputTokens != 12 || records[0].Detail.OutputTokens != 4 {
					t.Errorf("partial-argument accounting: %+v", records)
				}
				if len(records) == 1 {
					tools := records[0].Detail.ToolUsageDetails
					if tools == nil || tools.ToolCallsRequested == nil || *tools.ToolCallsRequested != 1 {
						t.Errorf("partial argument fragments lost or duplicated structured tool usage: %+v", tools)
					}
				}
				if len(records) == 1 && mode == "cancel" && records[0].Fail.StatusCode != 499 {
					t.Errorf("cancel classification changed: %+v", records[0].Fail)
				}
				if len(records) == 1 && mode == "incomplete" && records[0].Fail.Code != usage.FailureStreamTruncatedCode {
					t.Errorf("partial failure not request-scoped truncation: %+v", records[0].Fail)
				}
				if mode == "incomplete" {
					if len(records) == 1 && (records[0].Fail.StatusCode != http.StatusBadGateway || bytes.Contains([]byte(records[0].Fail.Body), []byte("apply_patch"))) {
						t.Errorf("ordinary partial arguments mislabeled: %+v", records[0].Fail)
					}
					current, _ := manager.GetByID(credential.ID)
					if current.Unavailable || current.Quota.Exceeded {
						t.Errorf("request-local truncation cooled credential: %+v", current)
					}
					for _, state := range current.ModelStates {
						if state.Unavailable || state.Quota.Exceeded {
							t.Errorf("request-local truncation cooled model: %+v", state)
						}
					}
				}
				if active, limit := manager.CredentialConcurrencySnapshot(credential.ID); active != 0 || limit != 10 {
					t.Errorf("settled capacity=%d/%d", active, limit)
				}
				if selectedHits.Load() != 1 || fallbackHits.Load() != 0 {
					t.Errorf("selected=%d fallback=%d", selectedHits.Load(), fallbackHits.Load())
				}
			})
		}
	}
}
