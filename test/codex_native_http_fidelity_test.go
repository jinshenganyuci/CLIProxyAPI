package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalcodex "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const nativeHTTPOutputItem = `{"type":"response.output_item.done","output_index":0,"item":{"id":"native-http-message","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`
const nativeHTTPMetadata = `{"type":"codex.response.metadata","headers":{"x-codex-turn-state":"opaque-next-state"},"future":{"nested":true}}`

func nativeHTTPCompletion(session string) string {
	return fmt.Sprintf(`{"type":"response.completed","response":{"id":"native-http-response","status":"completed","output":[],"metadata":{"prompt_cache_key":%q},"future":{"nested":true},"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, session)
}

func TestCodexNativeHTTPHandlerFidelityWithCredentialIdentity(t *testing.T) {
	for _, marker := range []string{"none", "header", "metadata"} {
		for _, identity := range []bool{false, true} {
			for _, buffering := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/identity=%t/buffering=%t", marker, identity, buffering), func(t *testing.T) {
					const model, session, namespace = "gpt-5.5", "native-http-session", "3cb59d69-5897-49ef-a82b-a4a47115a51e"
					captured := make(chan []byte, 1)
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Error(errRead)
							return
						}
						captured <- body
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: %s\n\n", nativeHTTPMetadata, nativeHTTPOutputItem, nativeHTTPCompletion(gjson.GetBytes(body, "prompt_cache_key").String()))
					}))
					defer upstream.Close()
					cfg := &config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: buffering, CredentialIdentity: config.CodexCredentialIdentityConfig{Enabled: identity}}}
					manager := coreauth.NewManager(nil, nil, nil)
					manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
					id := "native-http-" + t.Name()
					_, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
						ID: id, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct",
						Attributes: map[string]string{"base_url": upstream.URL},
						Metadata: map[string]any{"access_token": "synthetic-token", "auth_kind": "oauth",
							internalcodex.CredentialIdentityVersionMetadataKey:   internalcodex.CredentialIdentityCurrentVersion,
							internalcodex.CredentialIdentityNamespaceMetadataKey: namespace},
					})
					if errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					handler := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
					router := gin.New()
					router.POST("/v1/responses", handler.Responses)
					metadata := "{}"
					if marker == "metadata" {
						metadata = `{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}`
					}
					payload := fmt.Sprintf(`{"model":%q,"stream":true,"input":[],"prompt_cache_key":%q,"client_metadata":%s}`, model, session, metadata)
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
					request.Header.Set("Content-Type", "application/json")
					if marker == "header" {
						request.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
					}
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, request)
					if recorder.Code != http.StatusOK {
						t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
					}
					body := <-captured
					mappedSession := session
					if identity {
						auth, _ := manager.GetByID(id)
						ns, _, _ := internalcodex.ParseCredentialIdentity(auth.Metadata)
						mappedSession = internalcodex.DeriveCredentialIdentity(ns, "session", session)
					}
					if gjson.GetBytes(body, "prompt_cache_key").String() != mappedSession {
						t.Fatalf("credential identity not applied to request: %s", body)
					}
					var completed []byte
					for _, line := range bytes.Split(recorder.Body.Bytes(), []byte("\n")) {
						if !bytes.HasPrefix(line, []byte("data:")) {
							continue
						}
						data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
						if gjson.GetBytes(data, "type").String() == "response.completed" {
							completed = data
						}
					}
					if marker != "none" {
						var got, want any
						errDecode := json.Unmarshal(completed, &got)
						_ = json.Unmarshal([]byte(nativeHTTPCompletion(session)), &want)
						if gjson.GetBytes(body, "instructions").Exists() || errDecode != nil || !reflect.DeepEqual(got, want) || (!identity && string(completed) != nativeHTTPCompletion(session)) {
							t.Fatalf("native HTTP fidelity lost: request=%s completion=%s", body, completed)
						}
						if !strings.Contains(recorder.Body.String(), nativeHTTPMetadata) || !strings.Contains(recorder.Body.String(), nativeHTTPOutputItem) {
							t.Fatal("native metadata or output item changed")
						}
					} else if gjson.GetBytes(completed, "response.output.0.id").String() != "native-http-message" {
						t.Fatalf("ordinary Responses completion repair lost: %s", completed)
					}
				})
			}
		}
	}
}

type nativeHTTPAlternateExecutor struct{ coreauth.ProviderExecutor }

func (*nativeHTTPAlternateExecutor) Identifier() string { return "native-http-alternate" }
func (*nativeHTTPAlternateExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: " + nativeHTTPOutputItem + "\n\ndata: " + nativeHTTPCompletion("alternate") + "\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestResponsesLiteHTTPRetainsRepairForOtherProvider(t *testing.T) {
	const model, id = "native-http-alternate-model", "native-http-alternate-auth"
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &nativeHTTPAlternateExecutor{}
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: id, Provider: executor.Identifier(), Status: coreauth.StatusActive}); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(id, executor.Identifier(), []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	cfg := &config.Config{}
	handler := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.POST("/v1/responses", handler.Responses)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"input":[]}`, model)))
	request.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"output":[{"id":"native-http-message"`) {
		t.Fatalf("other-provider completion repair lost: HTTP %d %s", recorder.Code, recorder.Body.String())
	}
}
