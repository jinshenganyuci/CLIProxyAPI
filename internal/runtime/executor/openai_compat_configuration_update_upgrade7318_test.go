package executor

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatConfigurationUpdateMerge7318(t *testing.T) {
	const original = `{"model":"compat-update","reasoning":{"effort":"medium","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"preserve message"}]}`
	const current = `{"model":"compat-update","reasoning":{"effort":"medium","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"preserve message"}]}`
	for _, tc := range []struct {
		name, model, payload, wantEffort, pluginEffort string
		stream, compact, plugin, isCompat              bool
	}{
		{name: "chat uses current update", payload: current, wantEffort: "high"},
		{name: "chat stream uses current update", payload: current, wantEffort: "high", stream: true},
		{name: "chat suffix wins", model: "compat-update(medium)", payload: current, wantEffort: "medium"},
		{name: "chat stream suffix wins", model: "compat-update(medium)", payload: current, wantEffort: "medium", stream: true},
		{name: "compact uses current update", payload: current, wantEffort: "high", compact: true},
		{name: "compact plugin deletes update", payload: original, wantEffort: "medium", compact: true, plugin: true},
		{name: "compact plugin rewrites update", payload: original, wantEffort: "high", compact: true, plugin: true, pluginEffort: "high"},
		{name: "compact compatibility plugin deletes update", payload: original, wantEffort: "medium", compact: true, plugin: true, isCompat: true},
		{name: "compact compatibility plugin rewrites update", payload: original, wantEffort: "high", compact: true, plugin: true, isCompat: true, pluginEffort: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.plugin {
				sdktranslator.SetPluginHooks(&updateNormalizingHooks{effort: tc.pluginEffort})
				t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
			}
			bodies := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read upstream request: %v", errRead)
				}
				bodies <- body
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.compact {
					_, _ = io.WriteString(w, `{"id":"compact-7318","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"chatcmpl-7318","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			t.Cleanup(upstream.Close)
			model := tc.model
			if model == "" {
				model = "compat-update"
			}
			req := cliproxyexecutor.Request{
				Model: model, Payload: []byte(tc.payload),
				Metadata: map[string]any{
					"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{
						ID: "compat-update", IsCompat: tc.isCompat, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
					},
				},
			}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(original), Stream: tc.stream}
			if tc.compact {
				opts.Alt = "responses/compact"
				opts.ResponseFormat = sdktranslator.FormatOpenAIResponse
			}
			auth := &cliproxyauth.Auth{ID: "compat-upgrade-7318", Provider: "openai-compatibility", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "synthetic-test"}}
			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			if tc.stream {
				stream, errStream := executor.ExecuteStream(t.Context(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream: %v", errStream)
				}
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream: %v", chunk.Err)
					}
				}
			} else if _, errExecute := executor.Execute(t.Context(), auth, req, opts); errExecute != nil {
				t.Fatalf("Execute: %v", errExecute)
			}
			body := <-bodies
			path := "reasoning_effort"
			if tc.compact {
				path = "reasoning.effort"
				if got := gjson.GetBytes(body, "reasoning.summary").String(); got != "auto" {
					t.Fatalf("summary = %q; body=%s", got, body)
				}
			}
			if got := gjson.GetBytes(body, path).String(); got != tc.wantEffort {
				t.Fatalf("%s = %q, want %q; body=%s", path, got, tc.wantEffort, body)
			}
			if bytes.Contains(body, []byte("configuration_update")) || !bytes.Contains(body, []byte("preserve message")) {
				t.Fatalf("unsupported updates remain or user message was lost: %s", body)
			}
			if string(req.Payload) != tc.payload || string(opts.OriginalRequest) != original {
				t.Fatal("executor mutated the current or original request")
			}
		})
	}
}
