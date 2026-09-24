package redisqueue

import (
	"context"
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageQueueResponseModelIsOptionalAndDoesNotChangeAccounting(t *testing.T) {
	for _, tc := range []struct {
		name          string
		model         string
		responseModel string
		wantResponse  string
		failed        bool
	}{
		{name: "legacy-omission", model: "requested-model"},
		{name: "whitespace-omission", model: "requested-model", responseModel: " \t\n "},
		{name: "observed-different-model", model: "requested-model", responseModel: " served-model ", wantResponse: "served-model"},
		{name: "unknown-request-model", responseModel: "served-model", wantResponse: "served-model"},
		{name: "failed-attempt", model: "requested-model", responseModel: "served-model", wantResponse: "served-model", failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEnabledQueue(t, func() {
				record := coreusage.Record{
					// Keep execution identity fixed while comparing optional response metadata.
					RequestID: "73160000-0000-4000-8000-000000000011",
					Provider:  "codex", Model: tc.model, Alias: "client-alias", AuthID: "credential-a",
					RequestedAt: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC),
					Failed:      tc.failed, Stream: true,
					Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				}
				plugin := &usageQueuePlugin{}
				plugin.HandleUsage(context.Background(), record)
				baseline := popSinglePayload(t)

				record.ResponseModel = tc.responseModel
				manager := coreusage.NewManager(1)
				defer manager.Stop()
				var replacedCalls atomic.Int32
				manager.RegisterNamed("redis", pluginFunc(func(context.Context, coreusage.Record) {
					replacedCalls.Add(1)
				}))
				manager.RegisterNamed("redis", plugin)
				done := make(chan struct{})
				manager.Register(pluginFunc(func(context.Context, coreusage.Record) { close(done) }))
				manager.Publish(context.Background(), record)
				manager.Stop()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("usage manager did not deliver Redis record")
				}
				if replacedCalls.Load() != 0 {
					t.Fatal("named plugin replacement duplicated delivery")
				}
				payload := popSinglePayload(t)
				if tc.wantResponse == "" {
					requireMissingField(t, payload, "response_model")
				} else {
					requireStringField(t, payload, "response_model", tc.wantResponse)
				}
				wantModel := tc.model
				if wantModel == "" {
					wantModel = "unknown"
				}
				requireStringField(t, payload, "model", wantModel)
				requireStringField(t, payload, "alias", "client-alias")
				requireIntField(t, requireTokensPayload(t, payload), "total_tokens", 15)

				// Legacy consumers decode their existing fields and ignore additive metadata.
				encoded, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				var legacy struct {
					Model  string `json:"model"`
					Alias  string `json:"alias"`
					Tokens struct {
						Total int64 `json:"total_tokens"`
					} `json:"tokens"`
				}
				if err := json.Unmarshal(encoded, &legacy); err != nil {
					t.Fatal(err)
				}
				if legacy.Model != wantModel || legacy.Alias != "client-alias" || legacy.Tokens.Total != 15 {
					t.Fatalf("legacy decoding changed accounting: %+v", legacy)
				}
				delete(payload, "response_model")
				if !reflect.DeepEqual(payload, baseline) {
					t.Fatalf("response model changed existing queue fields\ngot: %s\nwant: %s", payload, baseline)
				}
			})
		})
	}
}
