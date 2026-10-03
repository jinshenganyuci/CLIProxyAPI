package pluginhost

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestResponsesServiceTierUsageThroughPinnedPluginManagerV812(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			provider, model := "v812-usage-provider", "v812-usage-model"
			capture := newTestUsageCapturePlugin(provider)
			registerTestUsagePlugin(t, t.Name(), capture)
			record := normalizeTestCapabilityRecord(capabilityRecord{id: "v812-usage-plugin"})
			host := newHostWithRecords(record)
			const payload = `{"id":"resp_synthetic","object":"response","status":"completed","service_tier":"default","usage":{"input_tokens":34,"output_tokens":499,"total_tokens":533}}`
			executor := &fakeExecutor{identifier: provider,
				execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					return pluginapi.ExecutorResponse{Payload: []byte(payload)}, nil
				},
				executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
					chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: " + payload + "\n\n")}
					close(chunks)
					return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
				},
			}
			adapter := newExecutorAdapterForRecordForTest(host, record, executor, []sdktranslator.Format{sdktranslator.FormatOpenAIResponse}, []sdktranslator.Format{sdktranslator.FormatOpenAIResponse})
			adapter.provider = provider
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(adapter)
			auth, errRegister := manager.Register(t.Context(), &coreauth.Auth{
				ID: t.Name(), Provider: provider, Status: coreauth.StatusActive, ProxyURL: "direct", Metadata: map[string]any{"concurrency_limit": 3},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			for range 2 {
				release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), auth)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				t.Cleanup(release)
			}
			ctx := coreusage.WithStream(t.Context(), stream)
			req := coreexecutor.Request{Model: model, Payload: []byte(`{"input":"synthetic"}`)}
			opts := coreexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: auth.ID, coreexecutor.ServiceTierMetadataKey: "priority"}}
			if stream {
				response, errStream := manager.ExecuteStream(ctx, []string{provider}, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range response.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, errExecute := manager.Execute(ctx, []string{provider}, req, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			usage := capture.waitRecord(t, 3*time.Second)
			if usage.AuthID != auth.ID || usage.Stream != stream || usage.Detail.InputTokens != 34 || usage.Detail.OutputTokens != 499 || usage.Detail.TotalTokens != 533 || usage.Detail.ResponseServiceTier != "default" || usage.ServiceTier != "priority" {
				t.Fatalf("usage attribution, token counts, or requested/reported tier changed: %+v", usage)
			}
			if inflight, limit := manager.CredentialConcurrencySnapshot(auth.ID); inflight != 2 || limit != 3 {
				t.Fatalf("completed request leaked its lease: %d/%d, want 2/3", inflight, limit)
			}
		})
	}
}
