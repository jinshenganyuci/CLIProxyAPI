package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"github.com/tidwall/gjson"
)

func TestV842ImageEffectiveTierMatchesFinalWireWithProxyAndLease(t *testing.T) {
	for _, provider := range []string{"codex", "openai-compatibility"} {
		for _, multipartBody := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/multipart_%t/stream_%t", provider, multipartBody, stream), func(t *testing.T) {
					const model = "gpt-image-1.5"
					authID := t.Name()
					var manager *coreauth.Manager
					var selectedHits, fallbackHits, wrappers, attempts atomic.Int32
					selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						selectedHits.Add(1)
						if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 1 || limit != 10 {
							t.Errorf("active lease = %d/%d, want 1/10", active, limit)
						}
						var tier, prompt string
						if multipartBody && provider != "codex" {
							if errParse := r.ParseMultipartForm(1 << 20); errParse != nil {
								t.Error(errParse)
								return
							}
							defer func() { _ = r.MultipartForm.RemoveAll() }()
							tier, prompt = r.FormValue("service_tier"), r.FormValue("prompt")
							file, _, errFile := r.FormFile("image")
							if errFile != nil {
								t.Error(errFile)
								return
							}
							data, errRead := io.ReadAll(file)
							_ = file.Close()
							if errRead != nil || string(data) != "synthetic-image-842" {
								t.Errorf("multipart file changed: %q, %v", data, errRead)
							}
						} else {
							body, errRead := io.ReadAll(r.Body)
							if errRead != nil {
								t.Error(errRead)
							}
							tier, prompt = gjson.GetBytes(body, "service_tier").String(), gjson.GetBytes(body, "prompt").String()
							if multipartBody && provider == "codex" {
								wantImage := "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString([]byte("synthetic-image-842"))
								if got := gjson.GetBytes(body, "images.0.image_url").String(); got != wantImage {
									t.Errorf("converted image changed: %q", got)
								}
							}
							if provider == "codex" && !multipartBody {
								identity := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String()
								if identity == "" || identity == "configured-install" || identity == "client-install" {
									t.Errorf("final identity mapping missing: %s", body)
								}
							}
						}
						if tier != "priority" || prompt != "configured-842" {
							t.Errorf("final wire tier/prompt = %q/%q", tier, prompt)
						}
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"AA==\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}\n\n")
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{"data":[{"b64_json":"AA=="}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`)
						}
					}))
					defer selected.Close()
					fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						fallbackHits.Add(1)
						http.Error(w, "fallback forbidden", http.StatusBadGateway)
					}))
					defer fallback.Close()
					cfg := codexCredentialIdentityTestConfig(false)
					cfg.ProxyURL = fallback.URL
					params := map[string]any{"service_tier": "priority", "prompt": "configured-842"}
					if !multipartBody {
						params["client_metadata.x-codex-installation-id"] = "configured-install"
					}
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: params}}
					auth := codexCredentialIdentityTestAuth(authID, "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
					auth.ID, auth.Provider, auth.ProxyURL = authID, provider, selected.URL
					auth.Attributes = map[string]string{"base_url": fallback.URL}
					var executor coreauth.ProviderExecutor = NewCodexExecutor(cfg)
					if provider != "codex" {
						auth.Attributes["api_key"] = "synthetic-842"
						executor = NewOpenAICompatExecutor(provider, cfg)
					}
					manager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
					manager.SetRetryConfig(0, 0, 0)
					manager.RegisterExecutor(executor)
					registered, errRegister := manager.Register(t.Context(), auth)
					if errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
					capture := &providerV810UsageCapture{authID: authID, records: make(chan usage.Record, 8)}
					usage.RegisterNamedPlugin(authID, capture)
					t.Cleanup(func() { usage.RegisterNamedPlugin(authID, antigravityUsageNoop{}) })
					opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, stream)
					payload := []byte(`{"model":"gpt-image-1.5","prompt":"client","service_tier":"auto","client_metadata":{"x-codex-installation-id":"client-install"}}`)
					if multipartBody {
						var body bytes.Buffer
						writer := multipart.NewWriter(&body)
						for key, value := range map[string]string{"model": model, "prompt": "client", "service_tier": "auto"} {
							if errWrite := writer.WriteField(key, value); errWrite != nil {
								t.Fatal(errWrite)
							}
						}
						part, errPart := writer.CreateFormFile("image", "test.png")
						if errPart != nil {
							t.Fatal(errPart)
						}
						_, _ = part.Write([]byte("synthetic-image-842"))
						if errClose := writer.Close(); errClose != nil {
							t.Fatal(errClose)
						}
						payload = body.Bytes()
						opts.Metadata[coreexecutor.RequestPathMetadataKey] = codexImagesEditsPath
						opts.Headers = http.Header{"Content-Type": []string{writer.FormDataContentType()}}
					}
					ctx := usage.WithServiceTier(context.Background(), "auto")
					ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
						wrappers.Add(1)
						return roundTripperFunc(func(r *http.Request) (*http.Response, error) { attempts.Add(1); return next.RoundTrip(r) })
					})
					req := coreexecutor.Request{Model: model, Payload: payload}
					if stream {
						result, errStream := manager.ExecuteStream(ctx, []string{provider}, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, errExecute := manager.Execute(ctx, []string{provider}, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
					if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 0 || limit != 10 {
						t.Fatalf("settled lease = %d/%d", active, limit)
					}
					release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), registered)
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					release()
					if selectedHits.Load() != 1 || fallbackHits.Load() != 0 || wrappers.Load() != 1 || attempts.Load() != 1 {
						t.Fatalf("route/wrapper/attempt counts = %d/%d/%d/%d", selectedHits.Load(), fallbackHits.Load(), wrappers.Load(), attempts.Load())
					}
					records := antigravityUsageRecordsAfterDelivery815(t, capture)
					if len(records) != 1 || records[0].Failed || records[0].ServiceTier != "auto" || records[0].EffectiveServiceTier != "priority" {
						t.Fatalf("wire priority but settled usage = %+v", records)
					}
				})
			}
		}
	}
}
