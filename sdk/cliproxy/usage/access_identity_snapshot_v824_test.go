package usage

import (
	"context"
	"testing"
	"time"
)

type reusedIdentityContextV824 struct {
	values map[string]any
	closed bool
}

func (c *reusedIdentityContextV824) Get(key string) (any, bool) {
	if c.closed {
		panic("usage consulted a released request context")
	}
	value, ok := c.values[key]
	return value, ok
}

func TestV824UsageIdentitySnapshotSurvivesContextReuseIncludingAnonymous(t *testing.T) {
	for _, tc := range []struct {
		name, key, provider string
		explicit            *bool
		wantNative          bool
	}{
		{name: "anonymous"},
		{name: "legacy-key-only", key: "synthetic-legacy"},
		{name: "native", key: "synthetic-native", provider: "config-inline", wantNative: true},
		{name: "plugin", key: "synthetic-plugin", provider: "access-guard"},
		{name: "explicit-false", key: "synthetic-parent", provider: "config-inline", explicit: GenerateFlag(false)},
		{name: "explicit-true", key: "synthetic-parent", provider: "custom", explicit: GenerateFlag(true), wantNative: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &reusedIdentityContextV824{values: map[string]any{"userApiKey": tc.key, "accessProvider": tc.provider}}
			ctx := context.WithValue(context.Background(), "gin", request)
			if tc.explicit != nil {
				ctx = WithIsNativeKey(ctx, *tc.explicit)
			}
			frozen := SnapshotAccessIdentity(ctx)
			// Releasing the request context must make every later usage path independent
			// of the pooled identity object, even when the original request was anonymous.
			request.values = map[string]any{"userApiKey": "synthetic-next-request", "accessProvider": "config-inline"}
			request.closed = true
			// Manager execution now recaptures identity; the frozen holder must
			// prevent that boundary from consulting the released Gin context.
			frozen = CaptureIdentity(frozen)
			child := WithAPIKey(frozen, "synthetic-child")
			if APIKeyFromContext(child) != "synthetic-child" || APIKeyFromContext(frozen) != tc.key {
				t.Fatal("child identity override mutated the parent snapshot")
			}
			manager := NewManager(1)
			defer manager.Stop()
			capture := &captureUsagePlugin{records: make(chan Record, 1)}
			manager.Register(capture)
			manager.Publish(frozen, Record{Model: "snapshot-model"})
			select {
			case got := <-capture.records:
				if got.APIKey != tc.key || got.AccessProvider != tc.provider || got.IsNativeKey != tc.wantNative {
					t.Fatalf("snapshot identity changed: key=%q provider=%q native=%t", got.APIKey, got.AccessProvider, got.IsNativeKey)
				}
			case <-time.After(time.Second):
				t.Fatal("usage record not delivered")
			}
		})
	}
}
