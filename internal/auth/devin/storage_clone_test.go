package devin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

func TestDevinCloneForMetadataUpdateDropsLoginGuardAndFrozenProxy(t *testing.T) {
	guardCalls := 0
	source := &oauthTokenStorage{
		metadata: map[string]any{"type": "devin", "session_token": "synthetic-session", "api_key": "synthetic-api", "user_id": "user", "proxy_url": "http://old.invalid", "nested": map[string]any{"value": "original"}},
		proxyURL: "http://old.invalid", explicit: false,
		beforeSave: func() error { guardCalls++; return fmt.Errorf("completed login") },
	}
	cloned, errClone := source.CloneForMetadataUpdate()
	if errClone != nil {
		t.Fatal(errClone)
	}
	storage := cloned.(*metadataTokenStorage)
	storage.metadata["nested"].(map[string]any)["value"] = "changed"
	if source.metadata["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("clone shares nested OAuth metadata")
	}
	storage.metadata["proxy_url"] = "http://new.invalid"
	storage.metadata["concurrency_limit"] = 3
	path := filepath.Join(t.TempDir(), "devin.json")
	if errSave := storage.SaveTokenToFile(path); errSave != nil {
		t.Fatal(errSave)
	}
	if guardCalls != 0 || source.proxyURL != "http://old.invalid" || source.metadata["proxy_url"] != "http://old.invalid" {
		t.Fatal("metadata save reused or changed completed login controls")
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved map[string]any
	if errDecode := json.Unmarshal(raw, &saved); errDecode != nil {
		t.Fatal(errDecode)
	}
	if saved["session_token"] != "synthetic-session" || saved["api_key"] != "synthetic-api" || saved["user_id"] != "user" || saved["proxy_url"] != "http://new.invalid" || saved["concurrency_limit"] != float64(3) {
		t.Fatal("metadata update lost native Devin credentials or operator controls")
	}
	recloned, errReclone := cloned.(baseauth.MetadataStorageCloner).CloneForMetadataUpdate()
	if errReclone != nil || recloned == cloned {
		t.Fatal("persisted Devin storage cannot be independently edited again")
	}
}
