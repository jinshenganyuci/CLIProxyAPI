package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialconcurrency"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type conversionV820Hook struct {
	coreauth.NoopHook
	onResult func(context.Context, coreauth.Result)
}

func (h conversionV820Hook) OnResult(ctx context.Context, result coreauth.Result) {
	h.onResult(ctx, result)
}

// Exercise native conversion through a real Manager and plugin adapter. A local
// refusal remains observable but cannot execute the plugin, rotate credentials,
// damage credential availability or release another request's admission lease.
func TestV820PluginConversionRefusalPreservesCredentialAccounting(t *testing.T) {
	for _, method := range []string{"execute", "stream", "count"} {
		for selected := 0; selected < 3; selected++ {
			for _, mixed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/credential=%d/mixed=%t", method, selected, mixed), func(t *testing.T) {
					provider := "conversion-" + strings.ToLower(t.Name())
					model := "conversion-model-" + method
					capture := newTestUsageCapturePlugin(provider)
					registerTestUsagePlugin(t, provider, capture)
					var calls, results atomic.Int32
					var manager *coreauth.Manager
					selectedID := fmt.Sprintf("%s/auth-%d", t.Name(), selected)
					limits := []int{10, 3, 10}
					hook := conversionV820Hook{onResult: func(ctx context.Context, result coreauth.Result) {
						results.Add(1)
						if result.AuthID != selectedID || result.Success != mixed {
							t.Errorf("result = %s/%t, want selected credential and success=%t", result.AuthID, result.Success, mixed)
						}
						if coreexecutor.UpstreamAttempted(ctx) != mixed {
							t.Errorf("upstream attempted = %t, want %t", coreexecutor.UpstreamAttempted(ctx), mixed)
						}
						if !mixed {
							if n, limit := manager.CredentialConcurrencySnapshot(selectedID); n != 2 || limit != limits[selected] {
								t.Errorf("refusal result admission = %d/%d, want 2/%d", n, limit, limits[selected])
							}
							if result.Error == nil || result.Error.StatusCode() != http.StatusBadRequest || !result.Error.IsRequestScoped() {
								t.Errorf("refusal lost request-scoped 400: %+v", result.Error)
							}
						}
					}}
					manager = coreauth.NewManager(nil, nil, hook)
					record := normalizeTestCapabilityRecord(capabilityRecord{id: provider})
					host := newHostWithRecords(record)
					checkCall := func(ctx context.Context, request pluginapi.ExecutorRequest) {
						calls.Add(1)
						if !mixed || request.AuthID != selectedID || request.SourceFormat != "claude" || !gjson.GetBytes(request.Payload, "messages.0.content").Exists() {
							t.Errorf("unexpected plugin call: auth=%s source=%s payload=%s", request.AuthID, request.SourceFormat, request.Payload)
						}
						if n, limit := manager.CredentialConcurrencySnapshot(selectedID); n != 2 || limit != limits[selected] {
							t.Errorf("plugin admission = %d/%d, want 2/%d", n, limit, limits[selected])
						}
						coreexecutor.MarkUpstreamAttempt(ctx)
					}
					payload := []byte(`{"choices":[{"message":{"role":"assistant","content":"accepted"}}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
					exec := &fakeExecutor{
						identifier: provider,
						execute: func(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
							checkCall(ctx, request)
							return pluginapi.ExecutorResponse{Payload: payload}, nil
						},
						executeStream: func(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
							checkCall(ctx, request)
							chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
							chunks <- pluginapi.ExecutorStreamChunk{Payload: append(append([]byte("data: "), payload...), '\n', '\n')}
							close(chunks)
							return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
						},
						countTokens: func(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
							checkCall(ctx, request)
							return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":2}`)}, nil
						},
					}
					adapter := newExecutorAdapterForRecordForTest(host, record, exec, []sdktranslator.Format{sdktranslator.FormatClaude}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
					adapter.provider = provider
					manager.RegisterExecutor(adapter)
					credentials := make([]*coreauth.Auth, 3)
					for i := range credentials {
						credential := &coreauth.Auth{ID: fmt.Sprintf("%s/auth-%d", t.Name(), i), Provider: provider, Status: coreauth.StatusActive, ProxyURL: "direct", Metadata: map[string]any{"access_token": fmt.Sprintf("synthetic-token-%d", i), "codex_identity_namespace": fmt.Sprintf("namespace-%d", i)}}
						if i == 1 {
							credential.Metadata[credentialconcurrency.Field] = 3
						}
						var errRegister error
						credentials[i], errRegister = manager.Register(context.Background(), credential)
						if errRegister != nil {
							t.Fatal(errRegister)
						}
						registry.GetGlobalRegistry().RegisterClient(credential.ID, provider, []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
					}
					releaseHeld, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), credentials[selected])
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					defer releaseHeld()
					content := `{"type":"file","file":{"file_id":"not-resolvable"}}`
					if mixed {
						content += `,{"type":"text","text":"keep this sendable turn"}`
					}
					req := coreexecutor.Request{Model: model, Payload: []byte(`{"model":"` + model + `","messages":[{"role":"user","content":[` + content + `]}]}`)}
					opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI, Stream: method == "stream", Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: selectedID}}
					var errExecute error
					switch method {
					case "execute":
						_, errExecute = manager.Execute(context.Background(), []string{provider}, req, opts)
					case "count":
						_, errExecute = manager.ExecuteCount(context.Background(), []string{provider}, req, opts)
					case "stream":
						var stream *coreexecutor.StreamResult
						stream, errExecute = manager.ExecuteStream(context.Background(), []string{provider}, req, opts)
						if stream != nil {
							for chunk := range stream.Chunks {
								if chunk.Err != nil {
									errExecute = chunk.Err
								}
							}
						}
					}
					var unsupported *translatorcommon.UnsupportedPartError
					if mixed && errExecute != nil || !mixed && (!errors.As(errExecute, &unsupported) || unsupported.Type != "file") {
						t.Fatalf("result error = %v, mixed=%t", errExecute, mixed)
					}
					wantCalls := int32(0)
					if mixed {
						wantCalls = 1
					}
					if calls.Load() != wantCalls || results.Load() != 1 || host.isPluginFused(record.id) {
						t.Errorf("plugin calls=%d results=%d fused=%t", calls.Load(), results.Load(), host.isPluginFused(record.id))
					}
					for i, original := range credentials {
						current, _ := manager.GetByID(original.ID)
						if current.Unavailable || current.Quota.Exceeded || !current.NextRetryAfter.IsZero() || current.LastError != nil || current.CredentialVersion != original.CredentialVersion || current.ProxyURL != original.ProxyURL || current.Metadata["codex_identity_namespace"] != original.Metadata["codex_identity_namespace"] {
							t.Errorf("conversion changed credential availability or identity: %+v", current)
						}
						wantHeld := 0
						var success, failure int64
						if i == selected {
							wantHeld = 1
							if mixed {
								success = 1
							} else {
								failure = 1
							}
						}
						if current.Success != success || current.Failed != failure {
							t.Errorf("credential %d counters=%d/%d, want %d/%d", i, current.Success, current.Failed, success, failure)
						}
						if n, limit := manager.CredentialConcurrencySnapshot(original.ID); n != wantHeld || limit != limits[i] {
							t.Errorf("final admission %d = %d/%d, want %d/%d", i, n, limit, wantHeld, limits[i])
						}
					}
					// The dispatcher is FIFO. A sentinel observes all synchronously
					// published failures without sleeping to assert an empty queue.
					if method != "count" {
						usage := capture.waitRecord(t, time.Second)
						if usage.Failed == mixed || usage.AuthID != selectedID || !mixed && (usage.Detail.TotalTokens != 0 || usage.Fail.StatusCode != http.StatusBadRequest) || mixed && usage.Detail.TotalTokens != 5 {
							t.Errorf("unexpected accounting: %+v", usage)
						}
					}
					coreusage.PublishRecord(context.Background(), coreusage.Record{Provider: provider, Source: "conversion-barrier"})
					if next := capture.waitRecord(t, time.Second); next.Source != "conversion-barrier" {
						t.Errorf("duplicate or unexpected count-token usage: %+v", next)
					}
					releaseHeld()
					if n, _ := manager.CredentialConcurrencySnapshot(selectedID); n != 0 {
						t.Errorf("held lease did not release: %d", n)
					}
				})
			}
		}
	}
}
