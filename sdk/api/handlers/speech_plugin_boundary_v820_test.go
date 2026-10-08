package handlers

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV820SpeechOnlyRestrictionAlsoAppliesToPluginExecutorRoutes(t *testing.T) {
	for _, kind := range []pluginapi.ModelRouteTargetKind{pluginapi.ModelRouteTargetExecutor, pluginapi.ModelRouteTargetSelf} {
		for _, stream := range []bool{false, true} {
			for _, entry := range []string{"openai", "openai-response", "openai-speech"} {
				t.Run(fmt.Sprintf("%s/stream=%t/entry=%s", kind, stream, entry), func(t *testing.T) {
					chunks := make(chan coreexecutor.StreamChunk, 1)
					chunks <- coreexecutor.StreamChunk{Payload: []byte("AUDIO")}
					close(chunks)
					host := &mockPluginUsageHost{execResp: coreexecutor.Response{Payload: []byte("AUDIO")}, streamResult: &coreexecutor.StreamResult{Chunks: chunks}}
					host.hasRouters = true
					host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
						return pluginapi.ModelRouteResponse{Handled: true, TargetKind: kind, Target: "v820-speech-plugin"}, true
					}
					handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
					handler.SetModelRouterHost(host)
					_, _, countErr := handler.ExecuteCountWithAuthManager(t.Context(), entry, "grok-tts", []byte(`{"text":"hello"}`), "")
					requireSpeechOnlyError(t, countErr)
					if host.lastPluginID != "" {
						t.Fatal("speech model reached the plugin token-count path")
					}
					request := ModelExecutionRequest{EntryProtocol: entry, ExitProtocol: "openai-speech", Model: "xai/grok-tts(auto)", Body: []byte(`{"text":"hello"}`), Stream: stream}
					if stream {
						response, errMsg := handler.ExecuteModelStream(t.Context(), request)
						if entry == "openai-speech" {
							if errMsg != nil {
								t.Fatal(errMsg)
							}
							for range response.Chunks {
							}
						} else {
							requireSpeechOnlyError(t, errMsg)
						}
					} else {
						response, errMsg := handler.ExecuteModel(t.Context(), request)
						if entry == "openai-speech" {
							if errMsg != nil || response.StatusCode != http.StatusOK {
								t.Fatalf("speech plugin entry rejected: %+v", errMsg)
							}
						} else {
							requireSpeechOnlyError(t, errMsg)
						}
					}
					if invoked := host.lastPluginID != ""; invoked != (entry == "openai-speech") {
						t.Fatalf("plugin execution bypassed endpoint restriction: invoked=%v", invoked)
					}
				})
			}
		}
	}
}
