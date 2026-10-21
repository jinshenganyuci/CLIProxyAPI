package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Duplicate key/base entries are intentional: the selected credential's proxy
// must identify its own model settings, without borrowing the sibling route.
func TestV837VertexModelSettingsKeepCredentialProxyAndFinalPayload(t *testing.T) {
	for _, credential := range []int{0, 1} {
		for _, alias := range []string{"public", "sibling"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("credential_%d/%s/stream_%t", credential, alias, stream), func(t *testing.T) {
					const model = "gemini-2.5-flash"
					var hits [2]atomic.Int32
					var fallbackHits, wrappers, attempts atomic.Int32
					var manager *coreauth.Manager
					ids := []string{t.Name() + "-a", t.Name() + "-b"}
					wantOpenAPI := credential == 0
					if alias == "sibling" {
						wantOpenAPI = !wantOpenAPI
					}
					locations := []string{"global", "us-central1"}
					fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						fallbackHits.Add(1)
						http.Error(w, "fallback forbidden", http.StatusBadGateway)
					}))
					defer fallback.Close()
					base := fallback.URL + "/v1/projects/p/locations/original/publishers/google/models"
					proxies := make([]*httptest.Server, 2)
					for i := range proxies {
						proxies[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							hits[i].Add(1)
							if active, limit := manager.CredentialConcurrencySnapshot(ids[i]); active != 1 || limit != 10 {
								t.Errorf("active lease = %d/%d", active, limit)
							}
							body, errRead := io.ReadAll(r.Body)
							if errRead != nil || gjson.GetBytes(body, "operator_marker").String() != "final837" {
								t.Errorf("payload rule missing: %s, err=%v", body, errRead)
							}
							wantPath := "/v1/projects/p/locations/original/publishers/google/models/" + model + ":generateContent"
							if stream {
								wantPath = strings.TrimSuffix(wantPath, ":generateContent") + ":streamGenerateContent"
							}
							if wantOpenAPI {
								wantPath = "/v1/projects/p/locations/" + locations[credential] + "/endpoints/openapi/chat/completions"
							}
							if r.URL.Path != wantPath {
								t.Errorf("route = %q, want %q", r.URL.Path, wantPath)
							}
							response := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`
							if wantOpenAPI {
								if r.Header.Get("Authorization") != "Bearer shared-key" {
									t.Error("partner request lost bearer credential")
								}
								response = `{"id":"chat837","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
								if stream {
									response = strings.ReplaceAll(strings.ReplaceAll(response, `"message":`, `"delta":`), `"chat.completion"`, `"chat.completion.chunk"`)
								}
							} else if r.Header.Get("X-Goog-Api-Key") != "shared-key" {
								t.Error("native request lost API key")
							}
							if stream {
								w.Header().Set("Content-Type", "text/event-stream")
								_, _ = io.WriteString(w, "data: "+response+"\n\ndata: [DONE]\n\n")
							} else {
								w.Header().Set("Content-Type", "application/json")
								_, _ = io.WriteString(w, response)
							}
						}))
						defer proxies[i].Close()
					}
					cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}, Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"operator_marker": "final837"}}}}}
					for i := range proxies {
						perModel, perCredential := i == 0, i != 0
						cfg.VertexCompatAPIKey = append(cfg.VertexCompatAPIKey, config.VertexCompatKey{APIKey: "shared-key", BaseURL: base, ProxyURL: proxies[i].URL, OpenAPI: &perCredential, OpenAPILocation: locations[i], Models: []config.VertexCompatModel{{Name: model, Alias: "public", OpenAPI: &perModel}}})
					}
					manager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
					manager.SetRetryConfig(0, 0, 0)
					manager.RegisterExecutor(NewGeminiVertexExecutor(cfg))
					var selected *coreauth.Auth
					for i, id := range ids {
						auth := &coreauth.Auth{ID: id, Provider: "vertex", ProxyURL: proxies[i].URL, Attributes: map[string]string{"api_key": "shared-key", "base_url": base}}
						registered, errRegister := manager.Register(t.Context(), auth)
						if errRegister != nil {
							t.Fatal(errRegister)
						}
						if i == credential {
							selected = registered
						}
						registry.GetGlobalRegistry().RegisterClient(id, "vertex", []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					}
					ctx := httptransport.WithDecorator(t.Context(), func(next http.RoundTripper) http.RoundTripper {
						wrappers.Add(1)
						return roundTripperFunc(func(r *http.Request) (*http.Response, error) { attempts.Add(1); return next.RoundTrip(r) })
					})
					opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: stream, Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: ids[credential], coreexecutor.RequestedModelMetadataKey: alias}}
					req := coreexecutor.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, alias))}
					if stream {
						result, errStream := manager.ExecuteStream(ctx, []string{"vertex"}, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, errExecute := manager.Execute(ctx, []string{"vertex"}, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
					if active, limit := manager.CredentialConcurrencySnapshot(ids[credential]); active != 0 || limit != 10 {
						t.Fatalf("settled lease = %d/%d", active, limit)
					}
					release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), selected)
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					release()
					if hits[credential].Load() != 1 || hits[1-credential].Load() != 0 || fallbackHits.Load() != 0 || wrappers.Load() != 1 || attempts.Load() != 1 {
						t.Fatalf("route/decorator counts = %d/%d/%d/%d/%d", hits[credential].Load(), hits[1-credential].Load(), fallbackHits.Load(), wrappers.Load(), attempts.Load())
					}
				})
			}
		}
	}
}
