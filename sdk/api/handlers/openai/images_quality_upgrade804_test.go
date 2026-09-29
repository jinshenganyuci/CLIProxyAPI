package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

type qualityUpgrade804Executor struct{ websocketDirectCaptureExecutor }

func (e *qualityUpgrade804Executor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	e.mu.Unlock()
	return coreexecutor.Response{Payload: []byte(`{"data":[{"b64_json":"AA=="}]}`)}, nil
}

func TestUpgrade804XAIQualityReachesExecutorAcrossImageEndpoints(t *testing.T) {
	for _, endpoint := range []string{"generation-json", "edit-json", "edit-multipart"} {
		for _, quality := range []string{" high ", " "} {
			t.Run(endpoint+"/"+quality, func(t *testing.T) {
				executor := &qualityUpgrade804Executor{websocketDirectCaptureExecutor: websocketDirectCaptureExecutor{provider: "xai"}}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.RegisterExecutor(executor)
				auth := &coreauth.Auth{ID: "quality-upgrade804", Provider: "xai", Status: coreauth.StatusActive, ProxyURL: "direct"}
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: xaiImages20Model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
				path, callback := imagesGenerationsPath, handler.ImagesGenerations
				contentType := "application/json"
				var body io.Reader
				if endpoint == "generation-json" {
					body = strings.NewReader(fmt.Sprintf(`{"model":%q,"prompt":"test","quality":%q}`, xaiImages20Model, quality))
				} else {
					path, callback = imagesEditsPath, handler.ImagesEdits
					if endpoint == "edit-json" {
						body = strings.NewReader(fmt.Sprintf(`{"model":%q,"prompt":"test","quality":%q,"image":"data:image/png;base64,AA=="}`, xaiImages20Model, quality))
					} else {
						var data bytes.Buffer
						writer := multipart.NewWriter(&data)
						for key, value := range map[string]string{"model": xaiImages20Model, "prompt": "test", "quality": quality} {
							if err := writer.WriteField(key, value); err != nil {
								t.Fatal(err)
							}
						}
						part, err := writer.CreateFormFile("image", "input.png")
						if err != nil {
							t.Fatal(err)
						}
						if _, err = part.Write([]byte{0x89, 'P', 'N', 'G', 13, 10, 26, 10}); err != nil {
							t.Fatal(err)
						}
						if err = writer.Close(); err != nil {
							t.Fatal(err)
						}
						contentType, body = writer.FormDataContentType(), &data
					}
				}
				resp := performImagesEndpointRequest(t, path, contentType, body, callback)
				if resp.Code != http.StatusOK {
					t.Fatalf("status=%d: %s", resp.Code, resp.Body.String())
				}
				payloads := executor.Payloads()
				if len(payloads) != 1 {
					t.Fatalf("executor calls=%d, want 1", len(payloads))
				}
				got := gjson.GetBytes(payloads[0], "quality")
				want := strings.TrimSpace(quality)
				if got.String() != want || got.Exists() != (want != "") {
					t.Fatalf("quality=%s, want %q; request=%s", got.Raw, want, payloads[0])
				}
				if endpoint != "generation-json" && !gjson.GetBytes(payloads[0], "image.url").Exists() {
					t.Fatalf("edit image disappeared: %s", payloads[0])
				}
			})
		}
	}
}
