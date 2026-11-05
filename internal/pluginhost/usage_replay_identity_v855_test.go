package pluginhost

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type releasedUsageIdentityV855 struct {
	values map[string]any
}

func (c *releasedUsageIdentityV855) Get(key string) (any, bool) {
	if c.values == nil {
		panic("usage replay consulted a released request context")
	}
	value, exists := c.values[key]
	return value, exists
}

func TestV855UsageReplayPreservesFrozenCallerIdentity(t *testing.T) {
	type observation struct {
		record    pluginapi.UsageRecord
		key       string
		provider  string
		native    bool
		hasNative bool
		canceled  bool
	}
	observed := make(chan observation, 3)
	var oldDelivered atomic.Int32
	oldRecord := normalizeTestCapabilityRecord(capabilityRecord{
		id: "identity-replay",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: usagePluginFunc(func(context.Context, pluginapi.UsageRecord) { oldDelivered.Add(1) }),
		}},
	})
	host := newHostWithRecords(oldRecord)
	t.Cleanup(host.ShutdownAll)
	if !host.beginUsageReplacement(context.Background(), oldRecord.id) {
		t.Fatal("could not quiesce an idle usage adapter")
	}
	adapter := &usageAdapter{host: host, pluginID: oldRecord.id, plugin: oldRecord.plugin.Capabilities.UsagePlugin}
	cases := []struct {
		id, key, provider string
		native            bool
	}{
		{id: "native", key: "synthetic-native", provider: "config-inline", native: true},
		{id: "plugin", key: "synthetic-plugin", provider: "access-guard"},
		{id: "anonymous"},
	}
	var submitted sync.WaitGroup
	for _, tc := range cases {
		tc := tc
		submitted.Go(func() {
			pooled := &releasedUsageIdentityV855{values: map[string]any{"userApiKey": tc.key, "accessProvider": tc.provider}}
			original := context.WithValue(context.Background(), "gin", pooled)
			ctx, cancel := context.WithCancel(coreusage.SnapshotAccessIdentity(original))
			pooled.values = nil
			cancel()
			headers := http.Header{"X-Usage-Proof": []string{tc.id}}
			adapter.HandleUsage(ctx, coreusage.Record{RequestID: tc.id, AuthID: "credential-" + tc.id, SelectionID: "selection-" + tc.id, ResponseHeaders: headers, Detail: coreusage.Detail{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}})
			headers.Set("X-Usage-Proof", "reused-after-enqueue")
		})
	}
	submitted.Wait()
	if oldDelivered.Load() != 0 {
		t.Fatal("usage reached the quiescing plugin")
	}
	replacement := normalizeTestCapabilityRecord(capabilityRecord{
		id: oldRecord.id, version: "replacement",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: usagePluginFunc(func(ctx context.Context, record pluginapi.UsageRecord) {
				native, explicit := coreusage.IsNativeKeyFromContext(ctx)
				observed <- observation{record: record, key: coreusage.APIKeyFromContext(ctx), provider: coreusage.AccessProviderFromContext(ctx), native: native, hasNative: explicit, canceled: ctx.Err() != nil}
			}),
		}},
	})
	setHostSnapshotForTest(host, true, replacement)
	host.finishUsageReplacement(oldRecord.id)
	got := make(map[string]observation)
	for range cases {
		select {
		case item := <-observed:
			if _, duplicate := got[item.record.RequestID]; duplicate {
				t.Fatalf("usage identity %s was replayed twice", item.record.RequestID)
			}
			got[item.record.RequestID] = item
		default:
			t.Fatal("held usage was not replayed on replacement completion")
		}
	}
	for _, tc := range cases {
		item := got[tc.id]
		if item.record.APIKey != tc.key || item.key != tc.key || item.record.AccessProvider != tc.provider || item.provider != tc.provider || item.record.IsNativeKey != tc.native || item.native != tc.native || !item.hasNative || item.canceled {
			t.Errorf("identity or cancellation leaked across replay for %s", tc.id)
		}
		if item.record.AuthID != "credential-"+tc.id || item.record.SelectionID != "selection-"+tc.id || item.record.ResponseHeaders.Get("X-Usage-Proof") != tc.id || item.record.Detail.TotalTokens != 7 {
			t.Errorf("usage attribution or header snapshot changed for %s", tc.id)
		}
	}
	select {
	case <-observed:
		t.Fatal("unexpected extra usage replay")
	default:
	}
}
