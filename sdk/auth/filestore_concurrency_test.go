package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialconcurrency"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestFileTokenStoreConcurrencyLimitRoundTrip(t *testing.T) {
	store := NewFileTokenStore()
	dir := t.TempDir()
	store.SetBaseDir(dir)
	auth := &coreauth.Auth{ID: "test.json", FileName: "test.json", Provider: "test", Metadata: map[string]any{"type": "test", credentialconcurrency.Field: 3}}
	if _, errSave := store.Save(context.Background(), auth); errSave != nil {
		t.Fatalf("save: %v", errSave)
	}
	for _, plugin := range []bool{false, true} {
		if plugin {
			RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
				return []*coreauth.Auth{
					{ID: "project-a", Provider: "test", Metadata: map[string]any{credentialconcurrency.Field: 20, "project": "a"}},
					{ID: "project-b", Provider: "test", Metadata: map[string]any{"project": "b"}},
				}, true, nil
			}))
			t.Cleanup(func() { RegisterPluginAuthParser(nil) })
		}
		auths, errList := store.List(context.Background())
		wantCount := 1
		if plugin {
			wantCount = 2
		}
		if errList != nil || len(auths) != wantCount {
			t.Fatalf("list: count=%d, error=%v", len(auths), errList)
		}
		for _, loaded := range auths {
			if got := coreauth.EffectiveConcurrencyLimit(loaded); got != 3 {
				t.Fatalf("loaded limit = %d, want 3", got)
			}
		}
	}
}

func TestFileTokenStoreRejectsInvalidConcurrencyLimitBeforeWriteOrPlugin(t *testing.T) {
	store := NewFileTokenStore()
	dir := t.TempDir()
	store.SetBaseDir(dir)
	path := filepath.Join(dir, "test.json")
	valid := []byte(`{"type":"test","access_token":"original","concurrency_limit":3}`)
	if errWrite := os.WriteFile(path, valid, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	for _, value := range []any{0, -1, 1.5, true, "", 1000001} {
		auth := &coreauth.Auth{ID: "test.json", FileName: "test.json", Provider: "test", Metadata: map[string]any{"type": "test", credentialconcurrency.Field: value}}
		if _, errSave := store.Save(context.Background(), auth); errSave == nil {
			t.Fatalf("invalid value %v was persisted", value)
		}
		actual, errRead := os.ReadFile(path)
		if errRead != nil || string(actual) != string(valid) {
			t.Fatalf("invalid save overwrote original: %v", errRead)
		}
	}
	if errWrite := os.WriteFile(path, []byte(`{"type":"test","concurrency_limit":0}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	called := false
	RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
		called = true
		return []*coreauth.Auth{{ID: "test", Provider: "test"}}, true, nil
	}))
	t.Cleanup(func() { RegisterPluginAuthParser(nil) })
	auths, errList := store.List(context.Background())
	if errList != nil || len(auths) != 0 || called {
		t.Fatalf("invalid file reached plugin: count=%d called=%t error=%v", len(auths), called, errList)
	}
}

func TestManagerReloginPreservesConcurrencyLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"test","access_token":"old","concurrency_limit":3}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	record := &coreauth.Auth{ID: "test.json", FileName: "test.json", Provider: "test", Metadata: map[string]any{"type": "test", "access_token": "new"}}
	mgr := NewManager(store, &dummyAuthenticator{provider: "test", record: record})
	if _, _, errLogin := mgr.Login(context.Background(), "test", &config.Config{AuthDir: dir}, nil); errLogin != nil {
		t.Fatalf("relogin: %v", errLogin)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved map[string]any
	if errDecode := json.Unmarshal(data, &saved); errDecode != nil {
		t.Fatal(errDecode)
	}
	if saved[credentialconcurrency.Field] != float64(3) || saved["access_token"] != "new" {
		t.Fatalf("relogin did not preserve limit with new credentials: %v", saved)
	}
}
