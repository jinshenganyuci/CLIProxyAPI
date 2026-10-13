package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUpgrade827UpdateTokenStoragePersistsRefreshWithoutRevertingNewerTokens(t *testing.T) {
	for _, newerDisk := range []bool{false, true} {
		t.Run(map[bool]string{false: "current_baseline", true: "newer_disk_rotation"}[newerDisk], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "codex.json")
			storage := &CodexTokenStorage{AccessToken: "old-access", RefreshToken: "old-refresh", IDToken: "old-id", AccountID: "account", Email: "synthetic@example.test", IdentityVersion: 1, IdentityNamespace: NewCredentialIdentityNamespace(), Metadata: map[string]any{"proxy_url": "direct", "concurrency_limit": 3}}
			if err := storage.SaveTokenToFile(path); err != nil {
				t.Fatal(err)
			}
			NewCodexAuth(nil).UpdateTokenStorage(storage, &CodexTokenData{AccessToken: "updated-access", Expire: "2099-01-01T00:00:00Z"})
			if newerDisk {
				newest := tokenStorageTestMap(t, storage)
				newest["access_token"], newest["refresh_token"] = "newest-access", "newest-refresh"
				if err := WithFileLock(path, func() error { return WriteJSONFileAtomic(path, newest) }); err != nil {
					t.Fatal(err)
				}
			}
			if err := storage.SaveTokenToFile(path); err != nil {
				t.Fatal(err)
			}
			raw, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			var saved map[string]any
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			wantAccess, wantRefresh := "updated-access", "old-refresh"
			if newerDisk {
				wantAccess, wantRefresh = "newest-access", "newest-refresh"
			}
			if saved["access_token"] != wantAccess || saved["refresh_token"] != wantRefresh || saved["id_token"] != "old-id" || saved["email"] != "synthetic@example.test" || saved[CredentialIdentityNamespaceMetadataKey] != storage.IdentityNamespace {
				t.Fatal("explicit refresh was lost or overwrote newer credentials/identity")
			}
			if storage.RefreshPatch != nil {
				t.Fatal("successful save retained a consumed refresh patch")
			}
			if !newerDisk {
				service := NewCodexAuth(nil)
				service.UpdateTokenStorage(storage, &CodexTokenData{AccessToken: "pending-access"})
				service.UpdateTokenStorage(storage, &CodexTokenData{AccessToken: "latest-pending-access"})
				if err := storage.SaveTokenToFile(path); err != nil {
					t.Fatal(err)
				}
				raw, errRead := os.ReadFile(path)
				if errRead != nil {
					t.Fatal(errRead)
				}
				if err := json.Unmarshal(raw, &saved); err != nil {
					t.Fatal(err)
				}
				if saved["access_token"] != "latest-pending-access" || saved["refresh_token"] != "old-refresh" || saved["proxy_url"] != "direct" || saved["concurrency_limit"] != float64(3) {
					t.Fatal("sequential or coalesced refresh lost token rotation or credential settings")
				}
			}
		})
	}
}
