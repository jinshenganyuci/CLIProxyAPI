package redisqueue_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUpgrade7316ExecutionIDsPreserveLegacyLogsAndSingleAccounting(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "active"
		if canceled {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			wasEnabled, wasUsage := redisqueue.Enabled(), redisqueue.UsageStatisticsEnabled()
			redisqueue.SetEnabled(true)
			redisqueue.SetUsageStatisticsEnabled(true)
			defer func() {
				redisqueue.SetEnabled(wasEnabled)
				redisqueue.SetUsageStatisticsEnabled(wasUsage)
			}()
			events, unsubscribe := redisqueue.SubscribeUsage()
			defer unsubscribe()
			<-events
			ctx := logging.WithRequestID(context.Background(), "00000000")
			ctx = usage.WithTraceID(ctx, "12345678")
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			if canceled {
				cancel()
			}
			reporter := helps.NewUsageReporter(ctx, "codex", "main", nil)
			reporter.Publish(ctx, usage.Detail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30})
			reporter.PublishAdditionalModel(ctx, "side", usage.Detail{InputTokens: 1, OutputTokens: 3, TotalTokens: 4})
			reporter.EnsurePublished(ctx)
			const contextID = "73160000-0000-4000-8000-000000000001"
			const recordID = "73160000-0000-4000-8000-000000000002"
			directCtx := usage.WithExecutionRequestID(ctx, contextID)
			usage.PublishRecord(directCtx, usage.Record{Provider: "codex", Model: "context", Detail: usage.Detail{TotalTokens: 2}})
			usage.PublishRecord(directCtx, usage.Record{Provider: "codex", Model: "record", RequestID: recordID, TraceID: "87654321", Detail: usage.Detail{TotalTokens: 1}})
			// The FIFO barrier proves that EnsurePublished did not emit another billable record.
			usage.PublishRecord(ctx, usage.Record{Provider: "codex", Model: "barrier"})
			payloads := make(map[string]map[string]any)
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
		read:
			for {
				select {
				case raw := <-events:
					var payload map[string]any
					if err := json.Unmarshal(raw, &payload); err != nil {
						t.Fatal(err)
					}
					model, _ := payload["model"].(string)
					if model == "barrier" {
						break read
					}
					if _, exists := payloads[model]; exists {
						t.Fatalf("duplicate usage for %q", model)
					}
					payloads[model] = payload
				case <-timer.C:
					t.Fatal("usage barrier not received")
				}
			}
			if len(payloads) != 4 {
				t.Fatalf("received %d model records, want 4", len(payloads))
			}
			seenIDs := map[string]bool{}
			for model, total := range map[string]float64{"main": 30, "side": 4, "context": 2, "record": 1} {
				payload := payloads[model]
				if payload["request_id"] != "00000000" {
					t.Fatalf("legacy log ID changed for %s: %v", model, payload["request_id"])
				}
				trace := "12345678"
				if model == "record" {
					trace = "87654321"
				}
				if payload["trace_id"] != trace {
					t.Fatalf("trace mismatch for %s: %v", model, payload["trace_id"])
				}
				id, _ := payload["execution_id"].(string)
				parsed, err := uuid.Parse(id)
				if err != nil || parsed.Version() != 4 || seenIDs[id] {
					t.Fatalf("invalid or repeated execution ID %q", id)
				}
				seenIDs[id] = true
				if model == "context" && id != contextID || model == "record" && id != recordID || model == "main" && id != reporter.RequestID() {
					t.Fatalf("execution ID precedence changed for %s: %s", model, id)
				}
				tokens, ok := payload["tokens"].(map[string]any)
				if !ok || tokens["total_tokens"] != total {
					t.Fatalf("billing changed for %s: %v", model, payload["tokens"])
				}
			}
		})
	}
}
