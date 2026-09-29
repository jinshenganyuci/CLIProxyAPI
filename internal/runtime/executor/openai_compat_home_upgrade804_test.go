package executor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestHome804SelectedTextOnlyImageResultPreserved(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			bodies := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
				}
				bodies <- body
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"synthetic","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				}
			}))
			t.Cleanup(server.Close)
			auth := &coreauth.Auth{ID: "home804", Provider: "home-compat", ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "auth_kind": "apikey", "base_url": server.URL, "compat_name": "home-compat"}, Metadata: map[string]any{
				"credential_options": map[string]any{"models": []config.OpenAICompatibilityModel{
					{Name: "upstream", Alias: "image-alias", InputModalities: []string{"text", "image"}},
					{Name: "upstream", Alias: "selected-alias", InputModalities: []string{"text"}, UseMaxCompletionTokens: true},
				}},
			}}
			wire, errMarshal := json.Marshal(map[string]any{"model": "upstream", "auth": auth, "model_info": map[string]any{"id": "upstream", "user_defined": true}})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.PublishHomeDispatch(compatOptionsHomeDispatcher(wire), executionregistry.New(), 1)
			manager.RegisterExecutor(NewOpenAICompatExecutor("openai-compatibility", cfg))
			payload := []byte(`{"model":"selected-alias","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"image","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}]}`)
			req := coreexecutor.Request{Model: "selected-alias", Payload: payload}
			opts := coreexecutor.Options{SourceFormat: translator.FormatClaude, ResponseFormat: translator.FormatOpenAI, Stream: stream}
			if stream {
				result, errStream := manager.ExecuteStream(t.Context(), []string{"home-compat"}, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, errExecute := manager.Execute(t.Context(), []string{"home-compat"}, req, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			body := <-bodies
			if strings.Contains(string(body), "image_url") || gjson.GetBytes(body, "max_completion_tokens").Int() != 64 {
				t.Fatalf("selected Home model options were not applied: %s", body)
			}
			found := false
			for _, message := range gjson.GetBytes(body, "messages").Array() {
				if message.Get("role").String() == "tool" && message.Get("tool_call_id").String() == "call" && message.Get("content").String() == "[image omitted: unsupported by upstream]" {
					found = true
				}
			}
			if !found {
				t.Fatalf("image-only tool result lost its required content: %s", body)
			}
		})
	}
}
