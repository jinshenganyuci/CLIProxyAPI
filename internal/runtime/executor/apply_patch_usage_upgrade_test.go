package executor

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestApplyPatchV810FailureRetainsObservedProviderUsage(t *testing.T) {
	for _, provider := range []string{"claude", "openai-compat", "kimi-chat", "kimi-responses", "meta", "devin"} {
		t.Run(provider, func(t *testing.T) {
			capture := &providerV810UsageCapture{authID: t.Name(), records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				var frames string
				switch provider {
				case "claude":
					frames = task6ProviderFixture("claude", "invalid", "apply_patch")
					frames = strings.Replace(frames, `"usage":{"input_tokens":5}`, `"usage":{"input_tokens":11,"output_tokens":22}`, 1)
				case "devin":
					var detail, frame []byte
					detail = protowire.AppendTag(detail, 2, protowire.VarintType)
					detail = protowire.AppendVarint(detail, 11)
					detail = protowire.AppendTag(detail, 3, protowire.VarintType)
					detail = protowire.AppendVarint(detail, 22)
					frame = protowire.AppendTag(frame, 7, protowire.BytesType)
					frame = protowire.AppendBytes(frame, detail)
					frames = string(append(helps.WrapConnectEnvelope(frame), task6DevinFrames(`{"input":7}`, false, `{}`)...))
				case "openai-compat", "kimi-chat":
					frames = "data: " + fmt.Sprintf(`{"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"apply_patch","arguments":%q}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}`, `{"input":7}`) + "\n\ndata: [DONE]\n\n"
				default:
					frames = "data: " + fmt.Sprintf(`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"function_call","id":"item","call_id":"call","name":"apply_patch","arguments":%q}],"usage":{"input_tokens":11,"output_tokens":22,"total_tokens":33}}}`, `{"input":7}`) + "\n\n"
				}
				_, _ = io.WriteString(w, frames)
			}))
			defer upstream.Close()
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://127.0.0.1:1"}}
			var exec cliproxyauth.ProviderExecutor
			switch provider {
			case "claude":
				exec = NewClaudeExecutor(cfg)
			case "kimi-chat", "kimi-responses":
				exec = NewKimiExecutor(cfg)
			case "meta":
				exec = NewMetaExecutor(cfg)
			case "devin":
				exec = NewDevinExecutor(cfg)
			default:
				exec = NewOpenAICompatExecutor("openai-compat", cfg)
			}
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: exec.Identifier(), ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": upstream.URL}}
			req := cliproxyexecutor.Request{Model: "test", Payload: []byte(task6PatchRequest)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true, OriginalRequest: req.Payload}
			if provider == "kimi-chat" {
				opts.SourceFormat = sdktranslator.FormatOpenAI
				req.Payload = []byte(`{"messages":[{"role":"user","content":"patch"}],"tools":[{"type":"custom","name":"apply_patch"}]}`)
			}
			result, errExecute := exec.ExecuteStream(t.Context(), auth, req, opts)
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			assertTask6FailedStream(t, result.Chunks)
			records := providerV810UsageRecords(t, capture)
			if len(records) != 1 {
				t.Fatalf("usage records=%d, want exactly one failed attempt", len(records))
			}
			record := records[0]
			if !record.Failed || record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 22 || record.Detail.TotalTokens != 33 {
				t.Fatalf("invalid patch discarded observed billing detail: failed=%v detail=%+v", record.Failed, record.Detail)
			}
		})
	}
}

func TestApplyPatchV810NonStreamFailureRetainsObservedProviderUsage(t *testing.T) {
	for _, provider := range []string{"claude", "openai-compat", "kimi-chat", "kimi-responses", "meta", "meta-sse", "devin"} {
		t.Run(provider, func(t *testing.T) {
			capture := &providerV810UsageCapture{authID: t.Name(), records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch provider {
				case "claude":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, task6ProviderFixture(provider, "invalid", "apply_patch"))
				case "meta-sse":
					_, _ = io.WriteString(w, "data: "+fmt.Sprintf(`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"function_call","id":"item","call_id":"call","name":"apply_patch","arguments":%q}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}`, `{"input":7}`)+"\n\n")
				case "meta", "kimi-responses":
					_, _ = io.WriteString(w, fmt.Sprintf(`{"id":"r","object":"response","status":"completed","output":[{"type":"function_call","id":"item","call_id":"call","name":"apply_patch","arguments":%q}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`, `{"input":7}`))
				case "devin":
					var detail, frame []byte
					detail = protowire.AppendTag(detail, 2, protowire.VarintType)
					detail = protowire.AppendVarint(detail, 5)
					detail = protowire.AppendTag(detail, 3, protowire.VarintType)
					detail = protowire.AppendVarint(detail, 3)
					frame = protowire.AppendTag(frame, 7, protowire.BytesType)
					frame = protowire.AppendBytes(frame, detail)
					_, _ = w.Write(append(helps.WrapConnectEnvelope(frame), task6DevinFrames(`{"input":7}`, false, `{}`)...))
				default:
					_, _ = io.WriteString(w, task6ProviderFixture(provider, "nonstream", "apply_patch"))
				}
			}))
			defer upstream.Close()
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://127.0.0.1:1"}}
			var exec cliproxyauth.ProviderExecutor
			switch provider {
			case "claude":
				exec = NewClaudeExecutor(cfg)
			case "kimi-chat", "kimi-responses":
				exec = NewKimiExecutor(cfg)
			case "meta", "meta-sse":
				exec = NewMetaExecutor(cfg)
			case "devin":
				exec = NewDevinExecutor(cfg)
			default:
				exec = NewOpenAICompatExecutor("openai-compat", cfg)
			}
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: exec.Identifier(), ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": upstream.URL}}
			req := cliproxyexecutor.Request{Model: "test", Payload: []byte(task6PatchRequest)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: req.Payload}
			if provider == "kimi-chat" {
				opts.SourceFormat = sdktranslator.FormatOpenAI
				req.Payload = []byte(`{"messages":[{"role":"user","content":"patch"}],"tools":[{"type":"custom","name":"apply_patch"}]}`)
			}
			_, errExecute := exec.Execute(t.Context(), auth, req, opts)
			assertTask6PatchError(t, errExecute)
			records := providerV810UsageRecords(t, capture)
			if len(records) != 1 {
				t.Fatalf("usage records=%d, want exactly one failed attempt", len(records))
			}
			record := records[0]
			if !record.Failed || record.Detail.InputTokens != 5 || record.Detail.OutputTokens != 3 || record.Detail.TotalTokens != 8 {
				t.Fatalf("invalid patch discarded observed billing detail: failed=%v detail=%+v", record.Failed, record.Detail)
			}
		})
	}
}
