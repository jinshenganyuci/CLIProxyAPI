package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV808PathInterceptorChainPreservesMetadataAndTermination(t *testing.T) {
	for _, afterAuth := range []bool{false, true} {
		for _, terminate := range []bool{false, true} {
			t.Run(fmt.Sprintf("after=%t/terminate=%t", afterAuth, terminate), func(t *testing.T) {
				const originalPath = "/v1/images/edits"
				const rewrittenPath = "/v1/images/generations"
				metadata := map[string]any{
					coreexecutor.RequestPathMetadataKey:  originalPath,
					coreexecutor.SelectedAuthMetadataKey: "selected-credential",
					coreexecutor.PinnedAuthMetadataKey:   "selected-credential",
					"nested":                             map[string]any{"credential_proxy": "direct"},
				}
				lastCalls := 0
				host := newHostWithRecords(
					capabilityRecord{id: "path-writer", priority: 30, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						RequestInterceptor: requestInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
							req.Metadata[coreexecutor.SelectedAuthMetadataKey] = "untrusted-credential"
							req.Metadata[coreexecutor.PinnedAuthMetadataKey] = "untrusted-credential"
							req.Metadata["nested"].(map[string]any)["credential_proxy"] = "untrusted-proxy"
							return pluginapi.RequestInterceptResponse{Path: " " + rewrittenPath + " "}, nil
						}),
					}}},
					capabilityRecord{id: "guard-observer", priority: 20, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						RequestInterceptor: requestInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
							if req.Metadata[coreexecutor.RequestPathMetadataKey] != rewrittenPath || req.Metadata[coreexecutor.SelectedAuthMetadataKey] != "selected-credential" || req.Metadata[coreexecutor.PinnedAuthMetadataKey] != "selected-credential" || req.Metadata["nested"].(map[string]any)["credential_proxy"] != "direct" {
								t.Error("plugin path propagation leaked private metadata mutations")
							}
							return pluginapi.RequestInterceptResponse{Path: " ", Terminate: terminate, StatusCode: http.StatusForbidden, ResponseBody: []byte(`{"error":"blocked"}`)}, nil
						}),
					}}},
					capabilityRecord{id: "terminal-observer", priority: 10, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						RequestInterceptor: requestInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
							lastCalls++
							if req.Metadata[coreexecutor.RequestPathMetadataKey] != rewrittenPath {
								t.Error("blank path cleared the previous override")
							}
							return pluginapi.RequestInterceptResponse{}, nil
						}),
					}}},
				)
				request := pluginapi.RequestInterceptRequest{SourceFormat: "openai-image", Model: "gpt-image-2.5", Metadata: metadata}
				var response pluginapi.RequestInterceptResponse
				if afterAuth {
					response = host.InterceptRequestAfterAuth(context.Background(), request)
				} else {
					response = host.InterceptRequestBeforeAuth(context.Background(), request)
				}
				if response.Path != rewrittenPath || response.Terminate != terminate || (lastCalls == 0) != terminate {
					t.Fatalf("path/termination contract changed: response=%+v lastCalls=%d", response, lastCalls)
				}
				if terminate && (response.StatusCode != http.StatusForbidden || string(response.ResponseBody) != `{"error":"blocked"}`) {
					t.Fatal("terminal response changed")
				}
				if metadata[coreexecutor.RequestPathMetadataKey] != originalPath || metadata[coreexecutor.SelectedAuthMetadataKey] != "selected-credential" || metadata[coreexecutor.PinnedAuthMetadataKey] != "selected-credential" || metadata["nested"].(map[string]any)["credential_proxy"] != "direct" {
					t.Fatal("path chain mutated caller metadata")
				}
			})
		}
	}
}
