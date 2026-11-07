package traffic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

func testCollector(t *testing.T, now func() time.Time, path string, reader func(string) (NetworkSample, error)) *Collector {
	t.Helper()
	if now == nil {
		now = func() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, beijing) }
	}
	if reader == nil {
		reader = func(string) (NetworkSample, error) { return NetworkSample{Epoch: "test-network"}, nil }
	}
	c, err := Open(Options{Enabled: true, Path: path, Now: now, ReadNetwork: reader, DisableBackground: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if errClose := c.Close(context.Background()); errClose != nil {
			t.Error(errClose)
		}
	})
	return c
}

func TestMeterPendingDatesIdentityFreezeAndMediaAfterFinish(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 59, 59, 0, beijing)
	c := testCollector(t, func() time.Time { return now }, "", nil)
	m := c.NewMeter()
	m.AddRx(HTTP, 11)
	if got := c.Summary().Business.Total.TotalBytes; got != 0 {
		t.Fatalf("pending attributed early: %d", got)
	}
	now = now.Add(2 * time.Second)
	m.AddTx(WebSocket, 13)
	m.Bind(Identity{Provider: "config-inline", Key: "native-secret-alpha"})
	m.Bind(Identity{Provider: "config-inline", Key: "must-not-rebind"})
	m.Finish()
	m.AddRx(RTP, 17)
	m.AddTx(DataChannel, 19)
	keys := c.Keys(KeyQuery{})
	if keys.Total != 1 {
		t.Fatalf("identity was rebound: %+v", keys)
	}
	series := c.Summary(Query{KeyID: keys.Keys[0].ID}).Business
	if series.Total.RxBytes != 28 || series.Total.TxBytes != 32 || len(series.Days) != 2 {
		t.Fatalf("bad traffic: %+v", series)
	}
	if series.Days[0].Period != "2026-10-31" || series.Days[0].RxBytes != 11 || series.Today.RxBytes != 17 {
		t.Fatalf("pending date changed: %+v", series)
	}
	if c.Summary().Unattributed.Total.TotalBytes != 0 {
		t.Fatal("pending was counted as unknown and again after bind")
	}
	var nilMeter *Meter
	nilMeter.Bind(Identity{})
	nilMeter.AddRx(HTTP, 1)
	nilMeter.AddTx(HTTP, 1)
	nilMeter.Finish()
	if FromContext(WithMeter(nil, m)) != m || FromContext(nil) != nil {
		t.Fatal("context meter mismatch")
	}
}

func TestDirectoryScopesAndBoundIdentityDoNotOverrideMetadata(t *testing.T) {
	c := testCollector(t, nil, "", nil)
	c.SyncNativeKeys([]Identity{{Key: "same-raw-key", Name: "Native", Disabled: true}})
	c.SyncPluginKeys("plugin:access-guard:a", []Identity{{Key: "same-raw-key", Name: "Plugin"}})
	m := c.NewMeter()
	m.Bind(Identity{Provider: "config-api-key", Key: "same-raw-key", Name: "request must not rename"})
	m.AddRx(HTTP, 3)
	c.SyncPluginKeys("plugin:access-guard:a", nil)
	rows := c.Keys(KeyQuery{IncludeDeleted: true})
	if rows.Total != 2 {
		t.Fatalf("provider identities collided: %+v", rows)
	}
	for _, row := range rows.Keys {
		if row.Group == "native" && (row.Name != "Native" || row.Enabled || row.Deleted || row.Traffic.Total.RxBytes != 3) {
			t.Fatalf("native directory overwritten: %+v", row)
		}
		if row.Group == "plugin" && !row.Deleted {
			t.Fatal("plugin removal missing")
		}
	}
	c.SyncNativeKeys(nil)
	m2 := c.NewMeter()
	m2.Bind(Identity{Provider: "config-inline", Key: "same-raw-key"})
	m2.AddTx(HTTP, 5)
	if c.Keys(KeyQuery{}).Total != 0 {
		t.Fatal("request resurrected a removed directory entry")
	}
	u := c.NewMeter()
	u.AddRx(HTTP, 7)
	u.Finish()
	u.Finish()
	if got := c.Summary().Unattributed.Total.RxBytes; got != 7 {
		t.Fatalf("unknown counted twice: %d", got)
	}
}

func TestDeletedKeySummaryAndVisibleDirectoryPreserveTotals(t *testing.T) {
	c := testCollector(t, nil, "", nil)
	c.SyncNativeKeys([]Identity{
		{Key: "zero-secret", Name: "Zero traffic"},
		{Key: "disabled-secret", Name: "Disabled", Disabled: true},
	})
	const provider = "plugin:access-guard:directory"
	identity := Identity{Provider: provider, Key: "removed-secret", Name: "Removed"}
	c.SyncPluginKeys(provider, []Identity{identity})
	id := c.Keys(KeyQuery{Group: "plugin"}).Keys[0].ID
	m := c.NewMeter()
	m.Bind(identity)
	m.AddRx(HTTP, 41)
	m.AddTx(HTTP, 17)
	m.Finish()
	before := c.Summary()
	if before.KeyDeleted || c.Summary(Query{KeyID: id}).KeyDeleted {
		t.Fatal("existing or unfiltered summary marked deleted")
	}

	c.SyncPluginKeys(provider, nil)
	selected := c.Summary(Query{KeyID: id})
	if !selected.KeyDeleted || selected.KeyID != id || selected.State == "key_not_found" {
		t.Fatalf("deleted selection metadata missing: %+v", selected)
	}
	if selected.Business.Total.RxBytes != 41 || selected.Business.Total.TxBytes != 17 {
		t.Fatalf("deleted key history changed: %+v", selected.Business.Total)
	}
	all := c.Summary()
	if all.KeyDeleted || all.Business.Total.Bytes != before.Business.Total.Bytes ||
		all.Business.Today.Bytes != before.Business.Today.Bytes || all.Business.Month.Bytes != before.Business.Month.Bytes {
		t.Fatal("hiding a deleted key changed the global summary")
	}

	seen := make(map[string]bool)
	disabled := 0
	for offset := 0; offset < 2; offset++ {
		page := c.Keys(KeyQuery{IncludeDeleted: false, Offset: offset, Limit: 1})
		if page.Total != 2 || len(page.Keys) != 1 {
			t.Fatalf("visible directory pagination includes removed key: %+v", page)
		}
		row := page.Keys[0]
		if row.Deleted || row.ID == id || seen[row.ID] || row.Traffic.Total.TotalBytes != 0 {
			t.Fatalf("visible zero-traffic key missing or duplicated: %+v", row)
		}
		seen[row.ID] = true
		if !row.Enabled {
			disabled++
		}
		if c.Summary(Query{KeyID: row.ID}).KeyDeleted {
			t.Fatal("visible key marked deleted")
		}
	}
	if disabled != 1 || c.Keys(KeyQuery{IncludeDeleted: true}).Total != 3 {
		t.Fatal("disabled key or retained deleted history missing")
	}
	if missing := c.Summary(Query{KeyID: "missing"}); missing.State != "key_not_found" || missing.KeyDeleted {
		t.Fatal("missing key confused with retained deleted key")
	}

	c.SyncPluginKeys(provider, []Identity{identity})
	if restored := c.Summary(Query{KeyID: id}); restored.KeyDeleted || restored.Business.Total.RxBytes != 41 {
		t.Fatal("restoring a directory key did not clear deletion metadata or retain traffic")
	}
}

func TestConcurrentBindAndCounters(t *testing.T) {
	c := testCollector(t, nil, "", nil)
	m := c.NewMeter()
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 1000; j++ {
				m.AddRx(WebSocket, 3)
				m.AddTx(RTP, 5)
			}
		}()
	}
	m.Bind(Identity{Provider: "config-inline", Key: "parallel-key"})
	workers.Wait()
	m.Finish()
	row := c.Keys(KeyQuery{}).Keys[0]
	if row.Traffic.Total.RxBytes != 36000 || row.Traffic.Total.TxBytes != 60000 {
		t.Fatalf("lost concurrent bytes: %+v", row.Traffic.Total)
	}
	if c.Summary().Business.Total.TotalBytes != row.Traffic.Total.TotalBytes {
		t.Fatal("overall business counters differ")
	}
}

func TestPersistenceRestartEpochResetAndPause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.db")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, beijing)
	sample := NetworkSample{Epoch: "namespace-1", RxBytes: 100, TxBytes: 200}
	reader := func(string) (NetworkSample, error) { return sample, nil }
	c := testCollector(t, func() time.Time { return now }, path, reader)
	m := c.NewMeter()
	m.Bind(Identity{Provider: "config-inline", Key: "never-persist-this-secret", Name: "Example"})
	m.AddRx(HTTP, 31)
	id := c.Keys(KeyQuery{}).Keys[0].ID
	sample.RxBytes += 50
	sample.TxBytes += 70
	if err := c.SampleNetwork(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	sample.RxBytes += 10
	sample.TxBytes += 20
	now = now.Add(time.Minute)
	d := testCollector(t, func() time.Time { return now }, path, reader)
	if got := d.Summary().Network.Total; got.RxBytes != 60 || got.TxBytes != 90 {
		t.Fatalf("restart delta duplicated/lost: %+v", got)
	}
	if !d.HasKey(id) || d.Keys(KeyQuery{}).Keys[0].Traffic.Total.RxBytes != 31 {
		t.Fatal("identity/history not recovered")
	}
	if !d.Summary().StartedAt.Equal(c.Summary().StartedAt) {
		t.Fatal("collection origin reset")
	}
	d.Configure(false, "eth0")
	sample.RxBytes += 1000
	sample.TxBytes += 1000
	_ = d.SampleNetwork()
	d.Configure(true, "eth0")
	_ = d.SampleNetwork()
	if d.Summary().Network.Total.RxBytes != 60 {
		t.Fatal("paused network bytes included")
	}
	sample = NetworkSample{Epoch: "namespace-2", RxBytes: 8, TxBytes: 9}
	_ = d.SampleNetwork()
	sample.RxBytes += 2
	sample.TxBytes += 3
	_ = d.SampleNetwork()
	if got := d.Summary().Network.Total; got.RxBytes != 62 || got.TxBytes != 93 {
		t.Fatalf("epoch reset changed old totals: %+v", got)
	}
	sample.RxBytes = 1
	sample.TxBytes = 1
	_ = d.SampleNetwork()
	if d.Summary().Network.Total.RxBytes != 62 {
		t.Fatal("counter rollback subtracted historical bytes")
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "never-persist-this-secret") {
		t.Fatal("raw key stored in database")
	}
}

func TestRetentionKeepsLifetimeAndRemovesOldDeletedKeys(t *testing.T) {
	now := time.Date(2026, 9, 30, 23, 0, 0, 0, beijing)
	c := testCollector(t, func() time.Time { return now }, "", nil)
	m := c.NewMeter()
	m.Bind(Identity{Provider: "config-inline", Key: "old"})
	m.AddRx(HTTP, 41)
	c.SyncNativeKeys(nil)
	now = time.Date(2026, 11, 1, 0, 0, 0, 0, beijing)
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Keys(KeyQuery{IncludeDeleted: true}).Total != 0 {
		t.Fatal("old deleted key retained")
	}
	summary := c.Summary()
	if summary.Business.Total.RxBytes != 41 || len(summary.Business.Days) != 0 {
		t.Fatalf("retention erased lifetime or kept history: %+v", summary.Business)
	}
}

func TestFailedCheckpointRetriesAbsoluteCountsAndCorruptionIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.db")
	c := testCollector(t, nil, path, nil)
	m := c.NewMeter()
	m.Bind(Identity{Provider: "config-inline", Key: "retry-secret"})
	m.AddRx(HTTP, 53)
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("closed storage silently accepted a checkpoint")
	}
	store, err := kvstore.OpenBoltStore(path, kvstore.WithSweepInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	c.store = store
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := testCollector(t, nil, path, nil)
	if d.Keys(KeyQuery{}).Keys[0].Traffic.Total.RxBytes != 53 {
		t.Fatal("retry duplicated/lost counters")
	}
	if err := d.store.KVMSet(context.Background(), map[string][]byte{stateKey: []byte("corrupt")}); err != nil {
		t.Fatal(err)
	}
	if err := d.store.Close(); err != nil {
		t.Fatal(err)
	}
	d.store = nil
	broken, err := Open(Options{Enabled: true, Path: path, DisableBackground: true, ReadNetwork: func(string) (NetworkSample, error) { return NetworkSample{}, errors.New("unavailable") }})
	if err == nil || broken == nil || broken.Summary().State != "memory_only" {
		t.Fatal("corrupt history was not surfaced")
	}
	if err := broken.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	check, err := kvstore.OpenBoltStore(path, kvstore.WithSweepInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	raw, _, err := check.KVGet(context.Background(), stateKey)
	if err != nil || string(raw) != "corrupt" {
		t.Fatal("corrupt history was overwritten")
	}
}

type failedDeleteStore struct{ checkpointStore }

func (s failedDeleteStore) KVDel(context.Context, ...string) (int64, error) {
	return 0, errors.New("injected delete failure")
}

func TestExpiredKeyDeletionSurvivesFailedDeleteAndRestart(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, beijing)
	path := filepath.Join(t.TempDir(), "traffic.db")
	c := testCollector(t, func() time.Time { return now }, path, nil)
	m := c.NewMeter()
	m.Bind(Identity{Provider: "config-inline", Key: "expired-secret"})
	m.AddRx(HTTP, 71)
	id := c.Keys(KeyQuery{}).Keys[0].ID
	c.SyncNativeKeys(nil)
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 11, 1, 12, 0, 0, 0, beijing)
	c.store = failedDeleteStore{c.store}
	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("delete failure not reported")
	}
	if _, found, err := c.store.KVGet(context.Background(), keyPrefix+id); err != nil || !found {
		t.Fatal("failure fixture did not retain orphan")
	}
	// Simulate process exit after the checkpoint committed but before deletion.
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	c.store = nil
	d := testCollector(t, func() time.Time { return now }, path, nil)
	if d.HasKey(id) {
		t.Fatal("expired directory entry was restored")
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := d.store.KVGet(context.Background(), keyPrefix+id); err != nil || found {
		t.Fatal("durable GC did not retry deletion")
	}
	if d.Summary().Business.Total.RxBytes != 71 {
		t.Fatal("GC removed overall lifetime total")
	}
}

func TestCloseCancelledStillReleasesDatabase(t *testing.T) {
	var block atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var signal sync.Once
	now := func() time.Time {
		if block.Load() {
			signal.Do(func() { close(entered) })
			<-release
		}
		return time.Date(2026, 11, 7, 12, 0, 0, 0, beijing)
	}
	path := filepath.Join(t.TempDir(), "traffic.db")
	c, err := Open(Options{Enabled: true, Path: path, Now: now, DisableBackground: true, ReadNetwork: func(string) (NetworkSample, error) { return NetworkSample{Epoch: "test"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	m := c.NewMeter()
	m.Bind(Identity{Provider: "config-inline", Key: "closing"})
	block.Store(true)
	done := make(chan struct{})
	go func() { defer close(done); m.AddRx(HTTP, 1) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close error=%v", err)
	}
	close(release)
	<-done
	store, err := kvstore.OpenBoltStore(path, kvstore.WithSweepInterval(0))
	if err != nil {
		t.Fatalf("cancelled close retained DB lock: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkParserAndUnavailableSampler(t *testing.T) {
	raw := "Inter-| Receive | Transmit\n eth0: 123 1 0 0 0 0 0 0 456 2 0 0 0 0 0 0\n"
	rx, tx, err := parseNetworkCounters(raw, "eth0")
	if err != nil || rx != 123 || tx != 456 {
		t.Fatalf("parse=(%d,%d,%v)", rx, tx, err)
	}
	if _, _, err := parseNetworkCounters(raw, "missing"); err == nil {
		t.Fatal("missing interface became zero traffic")
	}
	if _, _, err := parseNetworkCounters("eth0: 1 2", "eth0"); err == nil {
		t.Fatal("truncated counter accepted")
	}
	c := testCollector(t, nil, "", func(string) (NetworkSample, error) { return NetworkSample{}, errors.New("not available") })
	if c.Summary().NetworkError == "" {
		t.Fatal("network failure omitted")
	}
}

func BenchmarkBoundMeterAdd(b *testing.B) {
	c, _ := Open(Options{Enabled: true, DisableBackground: true, ReadNetwork: func(string) (NetworkSample, error) { return NetworkSample{Epoch: "benchmark"}, nil }})
	defer c.Close(context.Background())
	m := c.NewMeter()
	m.Bind(Identity{Provider: "config-inline", Key: "benchmark-key"})
	m.AddRx(HTTP, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.AddRx(HTTP, 32768)
	}
}
