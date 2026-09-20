package pluginhost

import (
	"context"
	"testing"
	"time"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestPluginCacheWriteUpgrade739ResponseAndAccounting(t *testing.T) {
	for _, field := range []string{"cache_write_tokens", "cache_creation_tokens"} {
		t.Run(field, func(t *testing.T) {
			authID := t.Name()
			records := make(chan coreusage.Record, 4)
			registerTestUsagePlugin(t, authID, coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
				if record.AuthID == authID {
					records <- record
				}
			}))
			executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "cache-write-upgrade"})
			host := newHostWithRecords(executorRecord)
			payload := []byte(`{"id":"completion","object":"chat.completion","model":"served-model","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_tokens_details":{"cached_tokens":800,"` + field + `":150}}}`)
			exec := &fakeExecutor{identifier: "cache-write-upgrade", execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				return pluginapi.ExecutorResponse{Payload: payload}, nil
			}}
			adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
			ctx := coreusage.WithRequestedModelAlias(context.Background(), "billing-alias")
			response, errExecute := adapter.Execute(ctx, &coreauth.Auth{ID: authID, Provider: exec.identifier}, coreexecutor.Request{
				Model: "requested-model", Payload: []byte(`{"model":"requested-model","messages":[{"role":"user","content":"hello"}]}`),
			}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatClaude})
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			if got := gjson.GetBytes(response.Payload, "usage.input_tokens").Int(); got != 50 {
				t.Errorf("Claude uncached input=%d, want 50: %s", got, response.Payload)
			}
			if gjson.GetBytes(response.Payload, "usage.cache_read_input_tokens").Int() != 800 || gjson.GetBytes(response.Payload, "usage.cache_creation_input_tokens").Int() != 150 {
				t.Errorf("Claude cache categories changed: %s", response.Payload)
			}
			coreusage.PublishRecord(context.Background(), coreusage.Record{AuthID: authID, Model: "barrier"})
			receive := func() coreusage.Record {
				t.Helper()
				select {
				case record := <-records:
					return record
				case <-time.After(5 * time.Second):
					t.Fatal("usage barrier not received")
					return coreusage.Record{}
				}
			}
			record := receive()
			if record.Failed || record.Model != "requested-model" || record.Alias != "billing-alias" || record.ResponseModel != "served-model" {
				t.Errorf("billing/model identity changed: %+v", record)
			}
			breakdown := record.Detail.TokenBreakdown
			if record.Detail.InputTokens != 1000 || record.Detail.TotalTokens != 1200 || breakdown.Input.TotalTokens != 1000 || breakdown.Input.UncachedTokens != 50 || breakdown.Input.CacheReadTokens != 800 || breakdown.Input.CacheWriteTokens != 150 || breakdown.Output.TotalTokens != 200 {
				t.Errorf("raw usage double counted or lost categories: %+v", record.Detail)
			}
			if next := receive(); next.Model != "barrier" {
				t.Errorf("duplicate usage before barrier: %+v", next)
			}
		})
	}
}
