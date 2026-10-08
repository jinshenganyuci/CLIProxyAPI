package executor

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV820CodexUnsupportedTurnStopsEveryTransportBeforeProxy(t *testing.T) {
	for _, compat := range []bool{false, true} {
		for _, mode := range []string{"http", "compact", "http-stream", "websocket", "websocket-stream", "count"} {
			t.Run(fmt.Sprintf("%s/compat=%t", mode, compat), func(t *testing.T) {
				var selectedHits, globalHits atomic.Int32
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					selectedHits.Add(1)
					w.WriteHeader(http.StatusBadGateway)
				}))
				t.Cleanup(selected.Close)
				global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					globalHits.Add(1)
					w.WriteHeader(http.StatusBadGateway)
				}))
				t.Cleanup(global.Close)
				const model = "gpt-5-codex"
				const baseURL = "http://attachment-upstream.invalid"
				auth := codexCredentialIdentityTestAuth(t.Name(), "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
				auth.ProxyURL = selected.URL
				auth.Attributes = map[string]string{"api_key": "synthetic-attachment-key", "base_url": baseURL}
				before := auth.Clone()
				cfg := codexCredentialIdentityTestConfig(false)
				cfg.Codex.CredentialProxyPolicy = "require"
				cfg.ProxyURL = global.URL
				cfg.CodexKey = []config.CodexKey{{APIKey: "synthetic-attachment-key", BaseURL: baseURL, Models: []config.CodexModel{{Name: model, IsCompat: compat}}}}
				request := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"gpt-5-codex","messages":[{"role":"user","content":"earlier"},{"role":"assistant","content":"answered"},{"role":"user","content":[{"type":"container_upload","file_id":"synthetic-file-not-stored"}]}]}`)}
				request.Metadata = map[string]any{"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: model, IsCompat: compat}}
				options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				httpExecutor := NewCodexExecutor(cfg)
				wsExecutor := NewCodexWebsocketsExecutor(cfg)
				wsExecutor.store = &codexWebsocketSessionStore{}
				var errExecute error
				switch mode {
				case "http":
					_, errExecute = httpExecutor.Execute(t.Context(), auth, request, options)
				case "compact":
					// Compact accepts native Responses input. A dedicated transform
					// exercises its failure boundary without inventing a Claude route.
					options.SourceFormat = sdktranslator.Format(t.Name())
					sdktranslator.Register(options.SourceFormat, sdktranslator.FormatOpenAIResponse, func(_ string, body []byte, _ bool) ([]byte, error) {
						return body, &translatorcommon.UnsupportedPartError{Type: "container_upload"}
					}, sdktranslator.ResponseTransform{})
					options.Alt = "responses/compact"
					_, errExecute = httpExecutor.Execute(t.Context(), auth, request, options)
				case "http-stream":
					_, errExecute = httpExecutor.ExecuteStream(t.Context(), auth, request, options)
				case "websocket":
					_, errExecute = wsExecutor.Execute(t.Context(), auth, request, options)
				case "websocket-stream":
					_, errExecute = wsExecutor.ExecuteStream(t.Context(), auth, request, options)
				case "count":
					_, errExecute = httpExecutor.CountTokens(t.Context(), auth, request, options)
				}
				var unsupported *translatorcommon.UnsupportedPartError
				if !errors.As(errExecute, &unsupported) || unsupported.Type != "container_upload" || clienterror.HTTPStatusFromError(errExecute) != http.StatusBadRequest {
					t.Fatalf("err = %v, want a request-scoped unsupported attachment 400", errExecute)
				}
				if selectedHits.Load() != 0 || globalHits.Load() != 0 {
					t.Fatalf("refused attachment reached network: selected=%d global=%d", selectedHits.Load(), globalHits.Load())
				}
				if !reflect.DeepEqual(before.Metadata, auth.Metadata) || auth.ProxyURL != before.ProxyURL {
					t.Fatal("translation rejection mutated the credential identity or proxy")
				}
			})
		}
	}
}

func TestV820CodexWorkingTurnControlsTranslationErrors(t *testing.T) {
	invalid := []byte(`{"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"synthetic-file-not-stored"}]}]}`)
	valid := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"keep this working turn"},{"type":"container_upload","file_id":"synthetic-file-not-stored"}]}]}`)
	for _, compat := range []bool{false, true} {
		t.Run(fmt.Sprintf("compat=%t", compat), func(t *testing.T) {
			_, body, _, errTranslate := translateCodexRequestPairWithUpdateIntent(sdktranslator.FormatClaude, sdktranslator.FormatCodex, "gpt-5-codex", invalid, valid, true, compat)
			if errTranslate != nil || !gjson.ValidBytes(body) || !strings.Contains(string(body), "keep this working turn") || strings.Contains(string(body), "synthetic-file-not-stored") {
				t.Fatalf("valid working payload rejected or altered by the baseline: body=%s err=%v", body, errTranslate)
			}
			_, _, _, errTranslate = translateCodexRequestPairWithUpdateIntent(sdktranslator.FormatClaude, sdktranslator.FormatCodex, "gpt-5-codex", valid, invalid, true, compat)
			var unsupported *translatorcommon.UnsupportedPartError
			if !errors.As(errTranslate, &unsupported) || unsupported.Type != "container_upload" {
				t.Fatalf("valid baseline hid the invalid working turn: %v", errTranslate)
			}
		})
	}
}
