package usage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type responseModelUsagePluginFunc func(context.Context, Record)

func (fn responseModelUsagePluginFunc) HandleUsage(ctx context.Context, record Record) {
	fn(ctx, record)
}

func TestUsageManagerResponseModelPreservesAccountingAndSingleDispatch(t *testing.T) {
	const count = 64
	manager := NewManager(1)
	defer manager.Stop()
	records := make(chan Record, count+1)
	stale := make(chan Record, count+1)
	manager.RegisterNamed("billing", responseModelUsagePluginFunc(func(_ context.Context, record Record) {
		stale <- record
	}))
	manager.RegisterNamed("billing", responseModelUsagePluginFunc(func(_ context.Context, record Record) {
		records <- record
	}))
	done := make(chan struct{})
	manager.Register(responseModelUsagePluginFunc(func(_ context.Context, record Record) {
		if record.AuthID == "barrier" {
			close(done)
		}
	}))

	var publishers sync.WaitGroup
	for i := range count {
		publishers.Go(func() {
			record := Record{
				Provider: "codex", Model: "requested-model", Alias: "client-alias",
				AuthID: fmt.Sprintf("auth-%d", i), ResponseModel: fmt.Sprintf("served-%d", i),
				Detail: Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			}
			if i%2 == 0 {
				record.ResponseModel = ""
			}
			manager.Publish(context.Background(), record)
			record.Model = "mutated-after-publish"
			record.ResponseModel = "mutated-after-publish"
		})
	}
	publishers.Wait()
	manager.Publish(context.Background(), Record{AuthID: "barrier"})
	manager.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("usage manager did not drain through the barrier")
	}
	if len(stale) != 0 {
		t.Fatalf("replaced plugin received %d records", len(stale))
	}
	if len(records) != count+1 {
		t.Fatalf("delivered %d records, want %d including barrier", len(records), count+1)
	}
	seen := make(map[string]bool, count)
	for range count {
		record := <-records
		if seen[record.AuthID] {
			t.Fatalf("duplicate usage for %q", record.AuthID)
		}
		seen[record.AuthID] = true
		var index int
		if _, err := fmt.Sscanf(record.AuthID, "auth-%d", &index); err != nil {
			t.Fatalf("unexpected record identity %q: %v", record.AuthID, err)
		}
		wantResponse := ""
		if index%2 != 0 {
			wantResponse = fmt.Sprintf("served-%d", index)
		}
		if record.ResponseModel != wantResponse || record.Model != "requested-model" || record.Alias != "client-alias" {
			t.Fatalf("model metadata changed in queue: %+v", record)
		}
		if record.Detail != (Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}) {
			t.Fatalf("response model changed token accounting: %+v", record.Detail)
		}
	}
	if record := <-records; record.AuthID != "barrier" {
		t.Fatalf("last record = %q, want barrier", record.AuthID)
	}
}
