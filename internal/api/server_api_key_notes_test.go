package api

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/traffic"
)

func TestTrafficNativeKeyNoteRenamePreservesIdentityAndCounters(t *testing.T) {
	const first, second = "synthetic-native-noted", "synthetic-native-zero"
	collector := newTrafficAPITestCollector(t)
	cfg := &config.Config{
		SDKConfig:   config.SDKConfig{APIKeys: []string{first, second}},
		APIKeyNotes: map[string]string{config.APIKeyNoteID(first): "Phone", config.APIKeyNoteID(second): "Editor"},
	}
	server := &Server{cfg: cfg}
	server.syncTrafficKeys(context.Background(), collector)
	rows := collector.Keys(traffic.KeyQuery{})
	if rows.Total != 2 {
		t.Fatal("native note sync changed the key count")
	}
	var id string
	for _, row := range rows.Keys {
		if row.Name == "Phone" {
			id = row.ID
		}
	}
	if id == "" {
		t.Fatal("native note was not passed to traffic metadata")
	}
	meter := collector.NewMeter()
	meter.Bind(traffic.Identity{Provider: "config-inline", Key: first})
	meter.AddRx(traffic.HTTP, 37)
	meter.AddTx(traffic.HTTP, 11)
	meter.Finish()
	before := collector.Summary().Business.Total.Bytes
	cfg.APIKeys = []string{second, first}
	cfg.APIKeyNotes[config.APIKeyNoteID(first)] = "  Renamed phone  "
	server.syncTrafficKeys(context.Background(), collector)
	rows = collector.Keys(traffic.KeyQuery{})
	for _, row := range rows.Keys {
		if row.ID == id {
			if row.Name != "Renamed phone" || row.Traffic.Total.RxBytes != 37 || row.Traffic.Total.TxBytes != 11 {
				t.Fatal("renaming or reordering changed key identity or counters")
			}
		} else if row.Name != "Editor" || row.Traffic.Total.TotalBytes != 0 {
			t.Fatal("renaming a key changed another key")
		}
	}
	if rows.Total != 2 || collector.Summary().Business.Total.Bytes != before {
		t.Fatal("renaming created a new identity or changed the global total")
	}
}
