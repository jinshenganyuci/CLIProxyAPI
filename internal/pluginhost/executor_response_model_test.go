package pluginhost

import (
	"bytes"
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestPluginExecutorResponseModelObservesRawResponseOnce(t *testing.T) {
	translatedFormat := sdktranslator.Format("response-model-test-client")
	translatedPayload := []byte(`{"model":"client-translated-model","choices":[{"message":{"content":"translated"}}]}`)
	sdktranslator.Register(translatedFormat, sdktranslator.FormatOpenAI, nil, sdktranslator.ResponseTransform{
		NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte {
			return bytes.Clone(translatedPayload)
		},
	})
	for _, tc := range []struct {
		name       string
		modelField string
		wantModel  string
		translate  bool
	}{
		{name: "native", modelField: `"model":"upstream-served-model",`, wantModel: "upstream-served-model"},
		{name: "translated", modelField: `"model":"upstream-served-model",`, wantModel: "upstream-served-model", translate: true},
		{name: "missing-model"},
		{name: "proprietary-field-is-not-guessed", modelField: `"vendor_metadata":{"deployment":"private-model"},`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authID := t.Name()
			records := make(chan coreusage.Record, 8)
			registerTestUsagePlugin(t, authID, coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
				if record.AuthID == authID {
					records <- record
				}
			}))
			executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "response-model-executor"})
			host := newHostWithRecords(executorRecord)
			payload := []byte(`{` + tc.modelField + `"object":"chat.completion","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			exec := &fakeExecutor{
				identifier: "response-model-plugin-provider",
				execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					return pluginapi.ExecutorResponse{Payload: payload}, nil
				},
			}
			adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
				[]sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
			auth := &coreauth.Auth{ID: authID, Provider: exec.identifier, Attributes: map[string]string{"type": "oauth"}}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}
			if tc.translate {
				opts.ResponseFormat = translatedFormat
			}
			ctx := coreusage.WithRequestedModelAlias(context.Background(), "client-alias")
			resp, err := adapter.Execute(ctx, auth, coreexecutor.Request{
				Model: "requested-model", Payload: []byte(`{"model":"requested-model","messages":[{"role":"user","content":"hello"}]}`),
			}, opts)
			if err != nil {
				t.Fatal(err)
			}
			if tc.translate && !bytes.Equal(resp.Payload, translatedPayload) {
				t.Fatalf("response translation did not run: %s", resp.Payload)
			}
			coreusage.PublishRecord(context.Background(), coreusage.Record{AuthID: authID, Model: "barrier"})
			receive := func() coreusage.Record {
				t.Helper()
				select {
				case record := <-records:
					return record
				case <-time.After(5 * time.Second):
					t.Fatal("usage record did not reach the dispatcher barrier")
					return coreusage.Record{}
				}
			}
			record := receive()
			if record.ResponseModel != tc.wantModel {
				t.Errorf("response model = %q, want upstream %q", record.ResponseModel, tc.wantModel)
			}
			if record.Model != "requested-model" || record.Alias != "client-alias" || record.Detail.TotalTokens != 15 || record.Failed {
				t.Errorf("accounting changed: %+v", record)
			}
			if second := receive(); second.Model != "barrier" {
				t.Fatalf("duplicate usage published before barrier: %+v", second)
			}
		})
	}
}
