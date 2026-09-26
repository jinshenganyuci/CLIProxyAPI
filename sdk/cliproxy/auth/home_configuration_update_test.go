package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type configurationUpdateHomeDispatcher struct {
	payload []byte
}

func (d configurationUpdateHomeDispatcher) HeartbeatOK() bool       { return true }
func (d configurationUpdateHomeDispatcher) AbortAmbiguousDispatch() {}
func (d configurationUpdateHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return d.payload, nil
}

func TestHomeDispatchConfigurationUpdateCapabilityAndLegacyFallback(t *testing.T) {
	const model = "gpt-6-luna"
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name, capability string
				localSupport     bool
				oauth            bool
				wantSupport      bool
				modelInfoID      string
			}{
				{name: "explicit true overrides local false", capability: `,"support_configuration_update":true`, wantSupport: true},
				{name: "explicit false overrides local true", capability: `,"support_configuration_update":false`, localSupport: true},
				{name: "legacy preserves selected API-key true", localSupport: true, wantSupport: true},
				{name: "legacy preserves selected OAuth true", oauth: true, wantSupport: true},
				{name: "legacy unknown model fails closed", localSupport: true, modelInfoID: "unknown-home-model"},
				{name: "legacy unbound defaults false"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					infoID := tc.modelInfoID
					if infoID == "" {
						infoID = model
					}
					auth := configuredCapabilityTestAuth("home-update-"+tc.name, "home-update-key")
					auth.Provider = "codex"
					auth.Attributes[AttributeSource] = "config:codex[0]"
					if tc.oauth {
						auth.Attributes = map[string]string{AttributeAuthKind: AuthKindOAuth, "plan_type": "free"}
						auth.Metadata = map[string]any{"access_token": "fake-token"}
					}
					manager := NewManager(nil, nil, nil)
					cfg := &internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}}
					if tc.localSupport {
						cfg.CodexKey = []internalconfig.CodexKey{{APIKey: "home-update-key", Prefix: "tenant", Models: []internalconfig.CodexModel{{Name: model, SupportConfigurationUpdate: true}}}}
					}
					manager.SetConfig(cfg)
					payload, errMarshal := json.Marshal(homeAuthDispatchResponse{Model: model, Auth: *auth})
					if errMarshal != nil {
						t.Fatal(errMarshal)
					}
					var dispatch map[string]json.RawMessage
					if errUnmarshal := json.Unmarshal(payload, &dispatch); errUnmarshal != nil {
						t.Fatal(errUnmarshal)
					}
					dispatch["model_info"] = json.RawMessage(`{"id":"` + infoID + `"` + tc.capability + `}`)
					payload, errMarshal = json.Marshal(dispatch)
					if errMarshal != nil {
						t.Fatal(errMarshal)
					}
					manager.PublishHomeDispatch(configurationUpdateHomeDispatcher{payload: payload}, executionregistry.New(), 1)
					exec := &selectedCapabilityCaptureExecutor{provider: "codex"}
					manager.RegisterExecutor(exec)
					if tc.localSupport {
						registry.GetGlobalRegistry().RegisterClient("other-oauth-"+tc.name, "codex", []*registry.ModelInfo{{ID: model, SupportConfigurationUpdate: true}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("other-oauth-" + tc.name) })
					}
					requestModel := model
					if tc.localSupport {
						requestModel = "tenant/" + model
					}
					req := cliproxyexecutor.Request{Model: requestModel}
					switch mode {
					case "execute":
						if _, errExecute := manager.Execute(t.Context(), []string{"codex"}, req, cliproxyexecutor.Options{}); errExecute != nil {
							t.Fatalf("Execute() error = %v", errExecute)
						}
					case "count":
						if _, errCount := manager.ExecuteCount(t.Context(), []string{"codex"}, req, cliproxyexecutor.Options{}); errCount != nil {
							t.Fatalf("ExecuteCount() error = %v", errCount)
						}
					case "stream":
						stream, errStream := manager.ExecuteStream(t.Context(), []string{"codex"}, req, cliproxyexecutor.Options{})
						if errStream != nil {
							t.Fatalf("ExecuteStream() error = %v", errStream)
						}
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream chunk error = %v", chunk.Err)
							}
						}
					}
					if len(exec.requests) != 1 {
						t.Fatalf("executor requests = %d", len(exec.requests))
					}
					info, ok := ResolvedModelInfo(exec.requests[0])
					if !ok || info == nil || info.SupportConfigurationUpdate != tc.wantSupport {
						t.Fatalf("Home selected capability = (%+v, %t), want support=%t; metadata=%+v", info, ok, tc.wantSupport, exec.requests[0].Metadata[resolvedAPIKeyModelInfoMetadataKey])
					}
				})
			}
		})
	}
}
