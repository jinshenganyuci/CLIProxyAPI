package redisqueue

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageQueueSessionRolesRemainOptionalAndPreserveAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, kind    string
		fork, compact bool
	}{
		{name: "legacy"},
		{name: "fork", kind: " fork ", fork: true},
		{name: "compaction", kind: "compaction", compact: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEnabledQueue(t, func() {
				metadata := internallogging.ClientRequestMetadata{
					SessionID:       "e1fa4dc0-7311-4000-8000-000000000001",
					ParentSessionID: "e1fa4dc0-7311-4000-8000-000000000002",
				}
				record := coreusage.Record{
					// Compare role metadata for the same execution, not separately generated UUIDs.
					RequestID: "73160000-0000-4000-8000-000000000012",
					Provider:  "codex", Model: "requested", Alias: "billing-alias", APIKey: "synthetic-key",
					ResponseModel: "served", ServiceTier: "requested-tier", ResponseServiceTier: "reported-tier",
					RequestedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), Stream: true,
					Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				}
				plugin := &usageQueuePlugin{}
				plugin.HandleUsage(internallogging.WithClientRequestMetadata(context.Background(), metadata), record)
				baseline := popSinglePayload(t)
				metadata.NodeKind, metadata.IsFork, metadata.IsCompaction = tc.kind, tc.fork, tc.compact
				plugin.HandleUsage(internallogging.WithClientRequestMetadata(context.Background(), metadata), record)
				payload := popSinglePayload(t)
				if tc.kind == "" {
					requireMissingField(t, payload, "node_kind")
				} else {
					requireStringField(t, payload, "node_kind", tc.name)
				}
				if tc.fork {
					requireBoolField(t, payload, "is_fork", true)
				} else {
					requireMissingField(t, payload, "is_fork")
				}
				if tc.compact {
					requireBoolField(t, payload, "is_compaction", true)
				} else {
					requireMissingField(t, payload, "is_compaction")
				}
				encoded, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				var legacy struct {
					Model, Alias string
					Tokens       struct {
						Total int64 `json:"total_tokens"`
					}
				}
				if err := json.Unmarshal(encoded, &legacy); err != nil {
					t.Fatal(err)
				}
				if legacy.Model != "requested" || legacy.Alias != "billing-alias" || legacy.Tokens.Total != 15 {
					t.Fatalf("legacy accounting = %+v", legacy)
				}
				for _, field := range []string{"node_kind", "is_fork", "is_compaction"} {
					delete(payload, field)
				}
				if !reflect.DeepEqual(payload, baseline) {
					t.Fatalf("session roles altered existing usage fields: got=%v want=%v", payload, baseline)
				}
			})
		})
	}
}
