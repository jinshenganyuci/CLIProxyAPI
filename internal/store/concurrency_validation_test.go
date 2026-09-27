package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialconcurrency"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestTokenStoresRejectInvalidConcurrencyBeforeBackendAccess(t *testing.T) {
	for _, testCase := range []struct {
		name string
		save func(context.Context, *coreauth.Auth) (string, error)
	}{
		{"git", (&GitTokenStore{}).Save},
		{"object", (&ObjectTokenStore{}).Save},
		{"postgres", (&PostgresStore{}).Save},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, value := range []any{0, -1, 1.5, true, "", 1000001} {
				auth := &coreauth.Auth{ID: "test.json", Metadata: map[string]any{"type": "test", credentialconcurrency.Field: value}}
				_, errSave := testCase.save(context.Background(), auth)
				if errSave == nil || !strings.Contains(errSave.Error(), "concurrency limit") {
					t.Fatalf("invalid limit %v reached unconfigured backend: %v", value, errSave)
				}
			}
		})
	}
}

func TestMirroredTokenStoresValidateConcurrencyAtRead(t *testing.T) {
	for _, testCase := range []struct {
		name string
		read func(string, string) (*coreauth.Auth, error)
	}{
		{"git", (&GitTokenStore{}).readAuthFile},
		{"object", (&ObjectTokenStore{}).readAuthFile},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "test.json")
			if errWrite := os.WriteFile(path, []byte(`{"type":"test","concurrency_limit":0}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if auth, errRead := testCase.read(path, dir); errRead == nil || auth != nil {
				t.Fatalf("invalid limit was loaded: %v", errRead)
			}
			if errWrite := os.WriteFile(path, []byte(`{"type":"test","concurrency-limit":3}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			auth, errRead := testCase.read(path, dir)
			if errRead != nil || coreauth.EffectiveConcurrencyLimit(auth) != 3 {
				t.Fatalf("valid limit was not loaded: %v", errRead)
			}
		})
	}
}
