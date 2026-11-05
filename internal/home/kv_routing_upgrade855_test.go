package home

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

func TestV855HomeFailureDoesNotReadOrOverwriteLocalIdentityCache(t *testing.T) {
	ClearCurrent()
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	t.Cleanup(func() {
		ClearCurrent()
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Error(errShutdown)
		}
	})
	dbPath := filepath.Join(t.TempDir(), "identity-cache.db")
	if errConfigure := kvstore.ConfigureLocal(true, dbPath); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	ctx := context.Background()
	key := "cpa:test:" + HashKeyPart("synthetic-credential-namespace")
	const original = `{"namespace":"local-only","precision":9007199254740993}`
	if enabled, errSet := KVSetBytesRequired(ctx, key, []byte(original), 0); errSet != nil || !enabled {
		t.Fatalf("seed local cache: enabled=%v err=%v", enabled, errSet)
	}
	var requests atomic.Int64
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		requests.Add(1)
		return "-ERR controlled backend failure\r\n"
	})
	client.heartbeatOK.Store(true)
	SetCurrent(client)
	var value map[string]any
	if active, found, errGet := KVGetJSONRequired(ctx, key, &value); !active || found || errGet == nil {
		t.Fatalf("Home read failure fell back to local: active=%v found=%v err=%v", active, found, errGet)
	}
	if active, written, errSet := KVSetNXRequired(ctx, key+":new", []byte("home-only"), 0); !active || written || errSet == nil {
		t.Fatalf("Home write failure fell back to local: active=%v written=%v err=%v", active, written, errSet)
	}
	if requests.Load() < 2 {
		t.Fatalf("expected actual Home commands, got %d", requests.Load())
	}
	ClearCurrent()
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	if errConfigure := kvstore.ConfigureLocal(true, dbPath); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	local, active, errClient := CurrentKVClient()
	if errClient != nil || !active {
		t.Fatalf("reopen local cache: active=%v err=%v", active, errClient)
	}
	got, found, errGet := local.KVGet(ctx, key)
	if errGet != nil || !found || string(got) != original {
		t.Fatalf("local identity bytes changed across Home failure/reopen: found=%v err=%v value=%q", found, errGet, got)
	}
	if _, foundNew, errGetNew := local.KVGet(ctx, key+":new"); errGetNew != nil || foundNew {
		t.Fatalf("Home write leaked into local DB: found=%v err=%v", foundNew, errGetNew)
	}
}
