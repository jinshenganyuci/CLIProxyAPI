package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/credentialfile"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestMetaOAuthTokenKeepsNativeCredentialOmissionRules(t *testing.T) {
	for _, withNativeStorage := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata acquisition", true: "native storage acquisition"}[withNativeStorage], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meta-test.json")
			existing := map[string]any{
				"type": "meta", "auth_kind": "oauth", "access_token": "old-access",
				"api_key": "old-api", "dca_token": "old-dca", "token_type": "old-token-type",
				"expires_in": float64(3600), "expired": "old-expiry", "dca_expired": "old-dca-expiry",
				"dca_expires_at": float64(1900000000), "last_refresh": "old-refresh",
				"proxy_url": "direct", "weight": float64(5),
			}
			data, errMarshal := json.Marshal(existing)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			record := &coreauth.Auth{Metadata: map[string]any{
				"type": "meta", "auth_kind": "oauth", "access_token": "new-access",
				"token_type": "", "expires_in": 0, "expired": "", "last_refresh": "",
			}}
			if withNativeStorage {
				record.Storage = &MetaTokenStorage{AccessToken: "new-access"}
				// Native storage must win even when preexisting metadata disagrees.
				record.Metadata["access_token"] = "discarded-metadata-access"
				record.Metadata["api_key"] = "discarded-metadata-api"
			}
			if errBind := BindOAuthProxy(record, "", false); errBind != nil {
				t.Fatal(errBind)
			}
			coreauth.MergeExistingAuthMetadata(record, existing)
			record.Metadata["dca_expired"] = "discarded-hook-expiry"
			record.Metadata["hook_control"] = "retained"
			record.Storage.(*oauthTokenStorage).SetMetadata(record.Metadata)
			if errSave := record.Storage.SaveTokenToFile(path); errSave != nil {
				t.Fatal(errSave)
			}
			want := map[string]any{
				"type": "meta", "auth_kind": "oauth", "access_token": "new-access",
				"proxy_url": "direct", "weight": float64(5), "hook_control": "retained",
			}
			assertMetaOAuthTokenFile(t, path, want)
			if !reflect.DeepEqual(record.Metadata, want) {
				t.Fatal("canonical metadata differs from the native credential projection")
			}
		})
	}
}

func TestMetaOAuthTokenPersistsCompleteAcquiredCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "meta-test.json")
	token := &MetaTokenStorage{
		AccessToken: "new-access", DCAToken: "new-dca", APIKey: "new-api", TokenType: "Bearer",
		ExpiresIn: 120, Expired: "new-expiry", DCAExpired: "new-dca-expiry", DCAExpiresAt: 1900000000,
		LastRefresh: "new-refresh", BaseURL: "https://api.meta.ai/v1", Email: "local@example.invalid", Name: "Local",
	}
	record := &coreauth.Auth{Storage: token, Metadata: map[string]any{
		"base_url": token.BaseURL, "email": token.Email, "name": token.Name,
	}}
	const route = "socks5://127.0.0.1:1080"
	if errBind := BindOAuthProxy(record, route, true); errBind != nil {
		t.Fatal(errBind)
	}
	// Mutating the original native object must not alter the acquired snapshot.
	token.AccessToken = "discarded-later-mutation"
	if errSave := record.Storage.SaveTokenToFile(path); errSave != nil {
		t.Fatal(errSave)
	}
	want := map[string]any{
		"type": "meta", "auth_kind": "oauth", "access_token": "new-access", "dca_token": "new-dca",
		"api_key": "new-api", "token_type": "Bearer", "expires_in": float64(120), "expired": "new-expiry",
		"dca_expired": "new-dca-expiry", "dca_expires_at": float64(1900000000), "last_refresh": "new-refresh",
		"base_url": token.BaseURL, "email": token.Email, "name": token.Name, "proxy_url": route,
	}
	assertMetaOAuthTokenFile(t, path, want)
	if record.ProxyURL != route || !reflect.DeepEqual(record.Metadata, want) {
		t.Fatal("saved record lost the acquired proxy or credentials")
	}
}

func TestMetaOAuthTokenRejectsInvalidExistingFile(t *testing.T) {
	for _, original := range []string{"private-invalid-json", "null", "[]"} {
		t.Run(original, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meta-test.json")
			if errWrite := os.WriteFile(path, []byte(original), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			record := &coreauth.Auth{Storage: &MetaTokenStorage{AccessToken: "new-access"}}
			if errBind := BindOAuthProxy(record, "direct", true); errBind != nil {
				t.Fatal(errBind)
			}
			if errSave := record.Storage.SaveTokenToFile(path); errSave == nil {
				t.Fatal("invalid existing credential file was overwritten")
			}
			assertMetaOAuthTokenBytes(t, path, []byte(original))
		})
	}
}

func TestMetaCloneTokenStorageDropsLoginGuardAndSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta-test.json")
	guardCalls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	record := &coreauth.Auth{
		Storage:  &MetaTokenStorage{AccessToken: "login-access", APIKey: "login-api", DCAToken: "stable-dca"},
		Metadata: map[string]any{"email": "local@example.invalid", "plugin": map[string]any{"enabled": true}},
	}
	if errBind := BindOAuthProxy(record, "direct", true, func() error {
		guardCalls++
		return ctx.Err()
	}); errBind != nil {
		t.Fatal(errBind)
	}
	if errSave := record.Storage.SaveTokenToFile(path); errSave != nil {
		t.Fatal(errSave)
	}
	cancel()
	cloned := CloneTokenStorage(record.Storage)
	if cloned == nil || cloned.DCAToken != "stable-dca" || cloned.Email != "local@example.invalid" {
		t.Fatal("cloned login storage lost acquired tokens or profile")
	}
	cloned.AccessToken = "refresh-access"
	cloned.APIKey = "refresh-api"
	cloned.Metadata["plugin"].(map[string]any)["enabled"] = false
	if !record.Metadata["plugin"].(map[string]any)["enabled"].(bool) {
		t.Fatal("cloned metadata aliases the login storage")
	}
	if errSave := cloned.SaveTokenToFile(path); errSave != nil {
		t.Fatal(errSave)
	}
	if guardCalls != 2 {
		t.Fatalf("runtime save invoked the completed login guard: %d calls", guardCalls)
	}
	assertMetaOAuthTokenFile(t, path, map[string]any{
		"type": "meta", "auth_kind": "oauth", "access_token": "refresh-access", "api_key": "refresh-api",
		"dca_token": "stable-dca", "email": "local@example.invalid", "proxy_url": "direct",
		"plugin": map[string]any{"enabled": false},
	})
}

func TestMetaCloneNativeTokenStorageIsIndependent(t *testing.T) {
	source := &MetaTokenStorage{AccessToken: "original", Metadata: map[string]any{"nested": map[string]any{"value": "original"}}}
	cloned := CloneTokenStorage(source)
	if cloned == nil || cloned == source {
		t.Fatal("native storage was not cloned")
	}
	cloned.AccessToken = "changed"
	cloned.Metadata["nested"].(map[string]any)["value"] = "changed"
	if source.AccessToken != "original" || source.Metadata["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("native clone changed source credentials or metadata")
	}
	var absent *MetaTokenStorage
	if CloneTokenStorage(absent) != nil || CloneTokenStorage(nil) != nil {
		t.Fatal("absent storage produced credentials")
	}
}

func TestMetaStorageCredentialTokensUseAuthoritativeSnapshot(t *testing.T) {
	type credentialTokens interface {
		MetaCredentialTokens() (dcaToken, apiKey, accessToken string)
	}
	native := &MetaTokenStorage{DCAToken: "acquired-dca", APIKey: "acquired-api", AccessToken: "acquired-access"}
	record := &coreauth.Auth{Storage: native, Metadata: map[string]any{"dca_token": "discarded-metadata"}}
	if errBind := BindOAuthProxy(record, "direct", true); errBind != nil {
		t.Fatal(errBind)
	}
	for _, storage := range []credentialTokens{native, record.Storage.(*oauthTokenStorage)} {
		dcaToken, apiKey, accessToken := storage.MetaCredentialTokens()
		if dcaToken != "acquired-dca" || apiKey != "acquired-api" || accessToken != "acquired-access" {
			t.Fatal("storage identity did not use authoritative credentials")
		}
	}
	native.DCAToken = "mutated-native-dca"
	record.Metadata["api_key"] = "mutated-metadata-api"
	dcaToken, apiKey, accessToken := record.Storage.(credentialTokens).MetaCredentialTokens()
	if dcaToken != "acquired-dca" || apiKey != "acquired-api" || accessToken != "acquired-access" {
		t.Fatal("acquisition credential identity changed after unrelated mutation")
	}
	for _, storage := range []credentialTokens{(*MetaTokenStorage)(nil), (*oauthTokenStorage)(nil)} {
		dcaToken, apiKey, accessToken := storage.MetaCredentialTokens()
		if dcaToken != "" || apiKey != "" || accessToken != "" {
			t.Fatal("nil storage exposed credential identity")
		}
	}
}

func TestMetaOAuthTokenReadsLatestProxyUnderCredentialLock(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "implicit rejects changed proxy", true: "explicit replaces changed proxy"}[explicit], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meta-test.json")
			if errWrite := os.WriteFile(path, []byte(`{"access_token":"old-token","proxy_url":"direct"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			metadata := map[string]any{"access_token": "new-token", "retained": "new-metadata"}
			record := &coreauth.Auth{Metadata: metadata}
			if errBind := BindOAuthProxy(record, "direct", explicit); errBind != nil {
				t.Fatal(errBind)
			}
			unlock := credentialfile.Lock(path)
			locked := true
			started := make(chan struct{})
			completed := make(chan error, 1)
			joined := false
			defer func() {
				if locked {
					unlock()
				}
				if !joined {
					<-completed
				}
			}()
			go func() {
				close(started)
				completed <- record.Storage.SaveTokenToFile(path)
			}()
			<-started
			latest := []byte("{\n  \"access_token\": \"concurrent-token\", \"proxy_url\": \"http://127.0.0.1:10082\"\n}\n")
			if errWrite := os.WriteFile(path, latest, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			unlock()
			locked = false
			errSave := <-completed
			joined = true
			if !explicit {
				if !errors.Is(errSave, ErrOAuthProxyMismatch) {
					t.Fatalf("implicit login did not reject the latest proxy: %v", errSave)
				}
				assertMetaOAuthTokenBytes(t, path, latest)
				return
			}
			if errSave != nil {
				t.Fatal(errSave)
			}
			assertMetaOAuthTokenFile(t, path, map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "new-token", "retained": "new-metadata", "proxy_url": "direct"})
		})
	}
}

func TestMetaOAuthTokenMergesLatestControlsAndActualHookEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta-test.json")
	existing := map[string]any{
		"type": "meta", "api_key": "old-token", "user_id": "old-profile",
		"proxy_url": "http://127.0.0.1:10081", "disabled": false, "weight": float64(1),
		"plugin": map[string]any{"nested": map[string]any{"enabled": false, "level": float64(1)}},
		"label":  "old-label", "auth_index": "old-index", "deleted_canonically": "old-value",
		"hook_edit": map[string]any{"nested": "before-hook"}, "hook_delete": "before-hook",
	}
	original, errMarshal := json.Marshal(existing)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	record := &coreauth.Auth{Metadata: map[string]any{
		"type": "meta", "auth_kind": "oauth", "access_token": "fresh-token", "api_key": "fresh-token", "user_id": "fresh-profile",
	}}
	const selectedRoute = "http://127.0.0.1:10082"
	if errBind := BindOAuthProxy(record, selectedRoute, true); errBind != nil {
		t.Fatal(errBind)
	}
	coreauth.MergeExistingAuthMetadata(record, existing)
	storage := record.Storage.(*oauthTokenStorage)
	if storage.metadataSets != 1 {
		t.Fatal("generic metadata merge did not establish the first metadata snapshot")
	}
	record.Metadata["hook_added"] = map[string]any{"enabled": true}
	record.Metadata["hook_edit"].(map[string]any)["nested"] = "after-hook"
	delete(record.Metadata, "hook_delete")
	storage.SetMetadata(record.Metadata)
	latest := map[string]any{
		"type": "meta", "api_key": "concurrent-old-token", "user_id": "concurrent-old-profile",
		"proxy_url": "http://127.0.0.1:10083", "disabled": true, "weight": float64(7),
		"plugin": map[string]any{"nested": map[string]any{"enabled": true, "level": float64(9)}},
		"label":  "latest-label", "auth_index": "latest-index", "late_added": "latest-value",
		"hook_edit": map[string]any{"nested": "before-hook"}, "hook_delete": "before-hook",
	}
	latestBytes, errLatest := json.Marshal(latest)
	if errLatest != nil {
		t.Fatal(errLatest)
	}
	unlock := credentialfile.Lock(path)
	locked := true
	started := make(chan struct{})
	completed := make(chan error, 1)
	joined := false
	defer func() {
		if locked {
			unlock()
		}
		if !joined {
			<-completed
		}
	}()
	go func() {
		close(started)
		completed <- storage.SaveTokenToFile(path)
	}()
	<-started
	if errWrite := os.WriteFile(path, latestBytes, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	unlock()
	locked = false
	errSave := <-completed
	joined = true
	if errSave != nil {
		t.Fatal(errSave)
	}
	want := map[string]any{
		"type": "meta", "auth_kind": "oauth", "access_token": "fresh-token", "api_key": "fresh-token", "user_id": "fresh-profile",
		"proxy_url": selectedRoute, "disabled": true, "weight": float64(7),
		"plugin": map[string]any{"nested": map[string]any{"enabled": true, "level": float64(9)}},
		"label":  "latest-label", "auth_index": "latest-index", "late_added": "latest-value",
		"hook_added": map[string]any{"enabled": true}, "hook_edit": map[string]any{"nested": "after-hook"},
	}
	assertMetaOAuthTokenFile(t, path, want)
	if !reflect.DeepEqual(record.Metadata, want) {
		t.Fatal("saved authentication metadata does not match the canonical file")
	}
}

func TestMetaOAuthTokenCancellationWhileWaitingForLockPreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta-test.json")
	original := []byte("{\n  \"api_key\": \"old-token\", \"proxy_url\": \"direct\"\n}\n")
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	record := &coreauth.Auth{Metadata: map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "fresh-token", "api_key": "fresh-token"}}
	if errBind := BindOAuthProxy(record, "direct", true, ctx.Err); errBind != nil {
		t.Fatal(errBind)
	}
	unlock := credentialfile.Lock(path)
	locked := true
	started := make(chan struct{})
	completed := make(chan error, 1)
	joined := false
	defer func() {
		if locked {
			unlock()
		}
		if !joined {
			<-completed
		}
	}()
	go func() {
		close(started)
		completed <- record.Storage.SaveTokenToFile(path)
	}()
	<-started
	cancel()
	unlock()
	locked = false
	errSave := <-completed
	joined = true
	if !errors.Is(errSave, context.Canceled) {
		t.Fatalf("canceled login returned %v, want context.Canceled", errSave)
	}
	assertMetaOAuthTokenBytes(t, path, original)
}

func TestMetaOAuthTokenGuardBeforeRenamePreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta-test.json")
	original := []byte("{\n  \"api_key\": \"old-token\", \"proxy_url\": \"direct\"\n}\n")
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	errInvalidated := errors.New("login session invalidated")
	guardCalls := 0
	record := &coreauth.Auth{Metadata: map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "fresh-token", "api_key": "fresh-token"}}
	if errBind := BindOAuthProxy(record, "direct", true, func() error {
		guardCalls++
		if guardCalls == 2 {
			return errInvalidated
		}
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}
	if errSave := record.Storage.SaveTokenToFile(path); !errors.Is(errSave, errInvalidated) {
		t.Fatalf("second guard returned %v, want invalidation error", errSave)
	}
	if guardCalls != 2 {
		t.Fatalf("save guard called %d times, want 2", guardCalls)
	}
	assertMetaOAuthTokenBytes(t, path, original)
}

func assertMetaOAuthTokenFile(t *testing.T, path string, want map[string]any) {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var got map[string]any
	if errDecode := json.Unmarshal(data, &got); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted metadata = %#v, want %#v", got, want)
	}
	assertMetaOAuthTokenBytes(t, path, data)
}

func assertMetaOAuthTokenBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("credential bytes changed unexpectedly")
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatal(errStat)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credential permissions = %04o, want 0600", info.Mode().Perm())
	}
	temporary, errGlob := filepath.Glob(filepath.Join(filepath.Dir(path), ".meta-oauth-token-*.tmp"))
	if errGlob != nil || len(temporary) != 0 {
		t.Fatalf("temporary credential artifacts remain: %v, error %v", temporary, errGlob)
	}
}
