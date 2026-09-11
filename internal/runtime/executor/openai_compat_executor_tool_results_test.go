package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorToolResultContentByInputModalities(t *testing.T) {
	type testCase struct {
		name            string
		stream          bool
		inputModalities []string
		textOnly        bool
		payload         string
		wantToolContent []string
		wantUserImages  []string
		wantUserText    string
	}
	tests := []testCase{
		{name: "non-stream text-only", stream: false, inputModalities: []string{"text"}, textOnly: true},
		{name: "stream text-only", stream: true, inputModalities: []string{"text"}, textOnly: true},
		{name: "non-stream multimodal", stream: false, inputModalities: []string{"text", "image"}, textOnly: false},
		{name: "stream multimodal", stream: true, inputModalities: []string{"text", "image"}, textOnly: false},
		{name: "non-stream unspecified", stream: false, inputModalities: nil, textOnly: false},
		{name: "stream unspecified", stream: true, inputModalities: nil, textOnly: false},
	}
	const omitted = "[image omitted: unsupported by upstream]"
	for _, stream := range []bool{false, true} {
		mode := "non-stream"
		if stream {
			mode = "stream"
		}
		for _, content := range []string{
			`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]`,
			`{"type":"image","source":{"type":"url","url":"https://example.com/tool.png"}}`,
		} {
			tests = append(tests, testCase{name: mode + " image-only " + string(content[0]), stream: stream, inputModalities: []string{"text"}, textOnly: true,
				payload:         `{"model":"claude-client","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"inspect_image","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":` + content + `}]}]}`,
				wantToolContent: []string{omitted}})
		}
		tests = append(tests, testCase{name: mode + " preserves original user image", stream: stream, inputModalities: []string{"text"}, textOnly: true,
			payload:         `{"model":"claude-client","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"inspect_image","input":{}},{"type":"tool_use","id":"call_2","name":"inspect_image","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"image inspected"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]},{"type":"tool_result","tool_use_id":"call_2","content":{"type":"image","source":{"type":"url","url":"https://example.com/tool.png"}}},{"type":"text","text":"ordinary user text"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"BB=="}}]}]}`,
			wantToolContent: []string{"image inspected\n\n" + omitted, omitted}, wantUserImages: []string{"data:image/png;base64,BB=="}, wantUserText: "ordinary user text"})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				if tt.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			}))
			defer server.Close()

			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{
					Name: "compat",
					Models: []config.OpenAICompatibilityModel{{
						Name:            "mapped-model",
						Alias:           "claude-client",
						InputModalities: tt.inputModalities,
					}},
				}},
			})
			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url":     server.URL + "/v1",
					"api_key":      "test",
					"compat_name":  "compat",
					"provider_key": "compat",
				},
			}
			payload := []byte(`{"model":"claude-client","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"inspect_image","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"image inspected"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}]}`)
			if tt.payload != "" {
				payload = []byte(tt.payload)
			}
			originalPayload := bytes.Clone(payload)
			req := cliproxyexecutor.Request{Model: "mapped-model", Payload: payload}
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				ResponseFormat:  sdktranslator.FormatOpenAI,
				Stream:          tt.stream,
				OriginalRequest: bytes.Clone(payload),
			}

			if tt.stream {
				result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream error: %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error: %v", chunk.Err)
					}
				}
			} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
				t.Fatalf("Execute error: %v", errExecute)
			}

			var toolContents, userImages []string
			foundUserText := tt.wantUserText == ""
			for _, message := range gjson.GetBytes(gotBody, "messages").Array() {
				content := message.Get("content")
				if message.Get("role").String() == "tool" {
					if content.Type != gjson.String {
						t.Fatalf("tool content must remain a string: %s", gotBody)
					}
					toolContents = append(toolContents, content.String())
				}
				if message.Get("role").String() == "user" {
					for _, part := range content.Array() {
						if part.Get("type").String() == "image_url" {
							userImages = append(userImages, part.Get("image_url.url").String())
						}
						if part.Get("text").String() == tt.wantUserText {
							foundUserText = true
						}
					}
				}
			}
			wantToolContent := tt.wantToolContent
			if wantToolContent == nil {
				want := "image inspected"
				if tt.textOnly {
					want += "\n\n" + omitted
				}
				wantToolContent = []string{want}
			}
			wantImages := tt.wantUserImages
			if !tt.textOnly {
				wantImages = []string{"data:image/png;base64,AA=="}
			}
			if !reflect.DeepEqual(toolContents, wantToolContent) || !reflect.DeepEqual(userImages, wantImages) || !foundUserText {
				t.Fatalf("upstream tool/image content mismatch: tools=%q images=%q user_text_preserved=%v; body=%s", toolContents, userImages, foundUserText, gotBody)
			}
			if !bytes.Equal(req.Payload, originalPayload) || !bytes.Equal(opts.OriginalRequest, originalPayload) {
				t.Fatal("executor mutated the original request")
			}
		})
	}
}
