package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPluginCloneForMetadataUpdateIsolatesPayloadAndMetadata(t *testing.T) {
	source := &pluginTokenStorage{provider: "test", rawJSON: []byte(`{"access_token":"synthetic-token"}`), meta: map[string]any{"nested": map[string]any{"value": "original"}}}
	cloned, errClone := source.CloneForMetadataUpdate()
	if errClone != nil {
		t.Fatal(errClone)
	}
	storage := cloned.(*pluginTokenStorage)
	if !bytes.Equal(source.RawJSON(), storage.RawJSON()) {
		t.Fatal("plugin clone changed the original credential payload")
	}
	storage.rawJSON[0] = ' '
	storage.meta["nested"].(map[string]any)["value"] = "changed"
	if source.rawJSON[0] != '{' || source.meta["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("plugin clone shares mutable payload or nested metadata")
	}
}

func TestPluginMetadataConcurrencyResetDoesNotRestoreRawStorageLimit(t *testing.T) {
	directory := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(directory)
	manager := coreauth.NewManager(store, nil, nil)
	metadata := map[string]any{"type": "test-plugin", "access_token": "synthetic-token", "concurrency_limit": 3}
	storage := &pluginTokenStorage{provider: "test-plugin", rawJSON: []byte(`{"type":"test-plugin","access_token":"synthetic-token","concurrency_limit":3,"concurrency-limit":4}`), meta: metadata}
	registered, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "plugin.json", FileName: "plugin.json", Provider: "test-plugin", Status: coreauth.StatusActive,
		Storage: storage, Metadata: metadata,
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	if coreauth.IsPluginVirtualAuth(registered) {
		t.Fatal("fixture must exercise an editable non-virtual plugin credential")
	}
	updated, errUpdate := manager.UpdateAndPersistMetadata(context.Background(), registered.ID, func(latest *coreauth.Auth) error {
		delete(latest.Metadata, "concurrency_limit")
		delete(latest.Metadata, "concurrency-limit")
		delete(latest.Attributes, "concurrency_limit")
		return nil
	})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if coreauth.EffectiveConcurrencyLimit(updated) != 10 {
		t.Fatal("reset did not restore the default local concurrency limit")
	}
	raw, errRead := os.ReadFile(filepath.Join(directory, "plugin.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	var persisted map[string]any
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		t.Fatal(errDecode)
	}
	for _, key := range []string{"concurrency_limit", "concurrency-limit"} {
		if _, exists := persisted[key]; exists {
			t.Fatalf("raw plugin storage resurrected cleared %s", key)
		}
	}
	if persisted["access_token"] != "synthetic-token" {
		t.Fatal("reset lost the plugin credential payload")
	}
}
