package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestUpgrade848StructuredFailureUsageQueueSnapshot(t *testing.T) {
	withEnabledQueue(t, func() {
		ctx := internallogging.WithResponseStatusHolder(context.Background())
		internallogging.SetResponseStatus(ctx, http.StatusOK)
		detail := coreusage.Detail{
			InputTokens: 12, OutputTokens: 4, TotalTokens: 16,
			TextInputTokens: coreusage.Int64Ptr(12), ImageInputTokens: coreusage.Int64Ptr(0),
			WebSearchRequests: coreusage.Int64Ptr(0),
			CacheDetails:      &coreusage.CacheDetails{ByTTL: map[string]int64{"5m": 0}},
			ToolUsageDetails:  &coreusage.ToolUsageDetails{WebSearchCalls: coreusage.Int64Ptr(0), Extra: map[string]int64{"custom": 2}},
			Iterations:        coreusage.IterationsPtr([]coreusage.IterationUsage{{InputTokens: coreusage.Int64Ptr(12), OutputTokens: coreusage.Int64Ptr(4), CacheCreation: &coreusage.CacheDetails{ByTTL: map[string]int64{"1h": 0}}}}),
			CostDetails:       &coreusage.CostDetails{Cost: coreusage.Float64Ptr(0), IsBYOK: coreusage.BoolPtr(false)},
		}
		(&usageQueuePlugin{}).HandleUsage(ctx, coreusage.Record{
			Provider: "gemini", Model: "gemini-4-argon", AuthIndex: "chosen-credential", APIKey: "synthetic-api-key", AccessProvider: "plugin", Stream: true,
			Failed: true, Fail: coreusage.Failure{StatusCode: http.StatusBadGateway, Code: coreusage.FailureStreamTruncatedCode, Body: "incomplete function arguments"}, Detail: detail,
		})
		*detail.ImageInputTokens = 99
		*detail.WebSearchRequests = 99
		detail.CacheDetails.ByTTL["5m"] = 99
		detail.ToolUsageDetails.Extra["custom"] = 99
		(*detail.Iterations)[0].CacheCreation.ByTTL["1h"] = 99
		payload := popSinglePayload(t)
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		for path, want := range map[string]string{
			"tokens.input_tokens": "12", "tokens.output_tokens": "4", "tokens.total_tokens": "16", "tokens.image_input_tokens": "0", "web_search_requests": "0",
			"cache_details.by_ttl.5m": "0", "tool_usage_details.web_search_calls": "0", "tool_usage_details.extra.custom": "2", "iterations.0.cache_creation.by_ttl.1h": "0", "cost_details.cost": "0", "cost_details.is_byok": "false",
			"failed": "true", "stream": "true", "fail.status_code": "502",
		} {
			if got := gjson.GetBytes(raw, path); !got.Exists() || got.Raw != want {
				t.Errorf("%s = %s, want %s", path, got.Raw, want)
			}
		}
		if gjson.GetBytes(raw, "tokens.audio_input_tokens").Exists() || gjson.GetBytes(raw, "fail.code").String() != coreusage.FailureStreamTruncatedCode || gjson.GetBytes(raw, "auth_index").String() != "chosen-credential" || gjson.GetBytes(raw, "access_provider").String() != "plugin" {
			t.Fatalf("optional usage/failure/identity changed: %s", raw)
		}
	})
}
