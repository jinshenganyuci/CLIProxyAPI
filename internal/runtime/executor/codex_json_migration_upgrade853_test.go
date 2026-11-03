package executor

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestV853CodexDirectMultipartGenerationKeepsMediaFinalizer(t *testing.T) {
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	for key, value := range map[string]string{"model": "gpt-image-2", "prompt": "original"} {
		if errWrite := writer.WriteField(key, value); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	captured := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if errParse := r.ParseMultipartForm(1 << 20); errParse != nil {
			t.Error(errParse)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		captured <- r.FormValue("prompt")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"AA=="}]}`)
	}))
	defer server.Close()
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*", Protocol: "openai"}},
		Params: map[string]any{"prompt": "configured"},
	}}}}
	auth := &cliproxyauth.Auth{ID: "multipart853", Provider: "codex", ProxyURL: "direct", Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic"}}
	_, err := NewCodexExecutor(cfg).executeDirectOpenAIImage(t.Context(), auth, cliproxyexecutor.Request{
		Model: "gpt-image-2", Payload: form.Bytes(),
	}, cliproxyexecutor.Options{Headers: http.Header{"Content-Type": {writer.FormDataContentType()}}}, codexImagesGenerationsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := <-captured; got != "configured" {
		t.Fatalf("multipart prompt = %q, want final configured value", got)
	}
}

func TestV853CodexIdentityDocPreservesRawNumbersAndEscapedPaths(t *testing.T) {
	cfg := codexCredentialIdentityTestConfig(false)
	auth := codexCredentialIdentityTestAuth("doc-identity.json", "218e400e-10df-48a1-9dc3-7d86710a575e")
	body := []byte(`{"prompt_cache_key":"client-session","client_metadata":{"x-codex-turn-metadata":{"turn_id":"client-turn","revision":9007199254740993}},"large":18446744073709551615,"precise":1.00000000000000000001}`)
	doc := ojson.NewDoc(body)
	state, errIdentity := applyCodexIdentityBodyDoc(cfg, auth, body, doc)
	if errIdentity != nil {
		t.Fatal(errIdentity)
	}
	if got := doc.Get("prompt_cache_key").String(); got == "client-session" || got == "" {
		t.Fatal("credential namespace was not applied")
	}
	if doc.Get("large").Raw != "18446744073709551615" || doc.Get("precise").Raw != "1.00000000000000000001" || doc.Get("client_metadata.x-codex-turn-metadata.revision").Raw != "9007199254740993" {
		t.Fatal("identity mutation rewrote raw numeric values")
	}
	mapped := state.promptCacheKey
	response := []byte(fmt.Sprintf(`{"odd.key":[{"session_id":%q,"revision":9007199254740993}],"a\\b":{"thread_id":%q},"":{"session_id":%q},":meta":{"session_id":%q},"::meta":[{":entry":{"thread_id":%q}}],"large":18446744073709551615,"precise":1.00000000000000000001}`, mapped, mapped, mapped, mapped, mapped))
	exposed := applyCodexIdentityExposeResponsePayload(response, state)
	for _, path := range []string{`odd\.key.0.session_id`, `a\\b.thread_id`, `.session_id`, `:meta.session_id`, `::meta.0.:entry.thread_id`} {
		if got := ojson.GetBytes(exposed, path).String(); got != "client-session" {
			t.Errorf("escaped identity path %q = %q, want client-session", path, got)
		}
	}
	if ojson.GetBytes(exposed, "meta").Exists() {
		t.Fatal("identity mapping introduced a field by interpreting a literal colon key as path syntax")
	}
	for _, raw := range []string{"9007199254740993", "18446744073709551615", "1.00000000000000000001"} {
		if !bytes.Contains(exposed, []byte(raw)) {
			t.Errorf("response identity mapping changed number %s", raw)
		}
	}
}

func TestV853CodexOutputLimitFinalBarrierAcrossTransports(t *testing.T) {
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude} {
		for _, transport := range []string{"http", "http-stream", "websocket", "websocket-stream"} {
			for _, rule := range []string{"preserve", "override", "filter"} {
				t.Run(source.String()+"/"+transport+"/"+rule, func(t *testing.T) {
					captured := make(chan []byte, 1)
					upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var payload []byte
						var conn *websocket.Conn
						var errRead error
						if transport == "websocket" || transport == "websocket-stream" {
							conn, errRead = upgrader.Upgrade(w, r, nil)
							if errRead != nil {
								t.Error(errRead)
								return
							}
							defer func() { _ = conn.Close() }()
							_, payload, errRead = conn.ReadMessage()
						} else {
							payload, errRead = io.ReadAll(r.Body)
						}
						if errRead != nil {
							t.Error(errRead)
							return
						}
						captured <- payload
						terminal := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_doc853","object":"response","status":"completed","model":"gpt-5.5","output":[],"metadata":{"prompt_cache_key":%q},"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, ojson.GetBytes(payload, "prompt_cache_key").String()))
						if conn != nil {
							if errWrite := conn.WriteMessage(websocket.TextMessage, terminal); errWrite != nil {
								t.Error(errWrite)
							}
						} else {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "data: %s\n\n", terminal)
						}
					}))
					defer server.Close()
					cfg := codexCredentialIdentityTestConfig(false)
					match := []config.PayloadModelRule{{Name: "*", Protocol: "codex"}}
					cfg.Payload.Override = []config.PayloadRule{{Models: match, Params: map[string]any{"prompt_cache_key": "configured-session", "audit.calls.-1": "once"}}}
					cfg.Payload.OverrideRaw = []config.PayloadRule{{Models: match, Params: map[string]any{"audit.large": "9007199254740993"}}}
					if rule == "override" {
						cfg.Payload.Override[0].Params["max_output_tokens"] = 7
					}
					if rule == "filter" {
						cfg.Payload.Filter = []config.PayloadFilterRule{{Models: match, Params: []string{"max_output_tokens"}}}
					}
					auth := codexCredentialIdentityTestAuth("limit853.json", "218e400e-10df-48a1-9dc3-7d86710a575e")
					auth.Attributes = map[string]string{"base_url": server.URL}
					auth.ProxyURL = "direct"
					payload := []byte(`{"model":"gpt-5.5","max_output_tokens":17,"prompt_cache_key":"original-session","input":[{"role":"user","content":"hi"}]}`)
					if source == sdktranslator.FormatClaude {
						payload = []byte(`{"model":"gpt-5.5","max_tokens":17,"messages":[{"role":"user","content":"hi"}]}`)
					}
					req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: source, ResponseFormat: sdktranslator.FormatOpenAIResponse}
					var response []byte
					consume := func(result *cliproxyexecutor.StreamResult, err error) {
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							response = append(response, chunk.Payload...)
						}
					}
					switch transport {
					case "http":
						result, err := NewCodexExecutor(cfg).Execute(t.Context(), auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						response = result.Payload
					case "http-stream":
						consume(NewCodexExecutor(cfg).ExecuteStream(t.Context(), auth, req, opts))
					case "websocket":
						result, err := NewCodexWebsocketsExecutor(cfg).Execute(t.Context(), auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						response = result.Payload
					case "websocket-stream":
						consume(NewCodexWebsocketsExecutor(cfg).ExecuteStream(t.Context(), auth, req, opts))
					}
					body := <-captured
					limit := ojson.GetBytes(body, "max_output_tokens")
					if (rule == "filter" && limit.Exists()) || (rule == "override" && limit.Int() != 7) || (rule == "preserve" && limit.Int() != 17) {
						t.Fatalf("output limit crossed final rule barrier: rule=%s value=%s", rule, limit.Raw)
					}
					if ojson.GetBytes(body, "audit.calls").Raw != `["once"]` || ojson.GetBytes(body, "audit.large").Raw != "9007199254740993" {
						t.Fatal("payload rules ran repeatedly or lost raw number precision")
					}
					if mapped := ojson.GetBytes(body, "prompt_cache_key").String(); mapped == "configured-session" || mapped == "" {
						t.Fatal("configured final identity bypassed credential namespace")
					}
					if !bytes.Contains(response, []byte(`"prompt_cache_key":"configured-session"`)) {
						t.Fatalf("client response lost configured identity: %s", response)
					}
				})
			}
		}
	}
}
