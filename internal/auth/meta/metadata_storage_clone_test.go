package meta

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMetaCloneForMetadataUpdateDropsLoginGuardAndKeepsTokens(t *testing.T) {
	guardCalls := 0
	source := &oauthTokenStorage{
		metadata:    map[string]any{"proxy_url": "http://old.invalid", "nested": map[string]any{"value": "original"}},
		credentials: map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "synthetic-access", "dca_token": "synthetic-dca", "api_key": "synthetic-api"},
		proxyURL:    "http://old.invalid",
		beforeSave:  func() error { guardCalls++; return fmt.Errorf("completed login") },
	}
	cloned, errClone := source.CloneForMetadataUpdate()
	if errClone != nil {
		t.Fatal(errClone)
	}
	storage := cloned.(*MetaTokenStorage)
	storage.Metadata["nested"].(map[string]any)["value"] = "changed"
	storage.Metadata["proxy_url"] = "http://new.invalid"
	storage.Metadata["concurrency_limit"] = 3
	path := filepath.Join(t.TempDir(), "meta.json")
	if errSave := storage.SaveTokenToFile(path); errSave != nil {
		t.Fatal(errSave)
	}
	if guardCalls != 0 || source.metadata["nested"].(map[string]any)["value"] != "original" || source.metadata["proxy_url"] != "http://old.invalid" {
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
	if saved["access_token"] != "synthetic-access" || saved["dca_token"] != "synthetic-dca" || saved["api_key"] != "synthetic-api" || saved["proxy_url"] != "http://new.invalid" || saved["concurrency_limit"] != float64(3) {
		t.Fatal("metadata update lost native Meta tokens or current controls")
	}
	if _, exists := saved["expired"]; exists {
		t.Fatal("metadata update invented an omitted native expiry field")
	}
}
