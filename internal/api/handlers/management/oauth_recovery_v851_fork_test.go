package management

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV851OAuthAdoptionPreservesOpaqueTargetID(t *testing.T) {
	for _, hook := range []bool{false, true} {
		name := "immediate"
		if hook {
			name = "persisted_hook"
		}
		t.Run(name, func(t *testing.T) {
			h := newCodexOAuthProxyTestHandler(t, "", "require")
			h.authManager.SetConfig(h.cfg)
			path := filepath.Join(h.cfg.AuthDir, "team", "account.json")
			if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
				t.Fatal(errMkdir)
			}
			metadata := map[string]any{
				"type": "codex", "access_token": "old-access", "account_id": "selected-account",
				"proxy_url": "direct", "concurrency_limit": 2,
				"headers": map[string]any{"X-Old-Header": "remove"},
			}
			if errIdentity := codex.SetCredentialIdentity(metadata, codex.NewCredentialIdentityNamespace()); errIdentity != nil {
				t.Fatal(errIdentity)
			}
			target, errRegister := h.authManager.Register(context.Background(), &coreauth.Auth{
				ID: "opaque-portal-target", FileName: "account.json", Provider: "codex", ProxyURL: "direct",
				Metadata: metadata, Attributes: map[string]string{coreauth.AttributePath: path, "runtime-extension": "keep"},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			release, errAcquire := h.authManager.AcquireCredentialConcurrency(context.Background(), target)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer release()
			var received *coreauth.Auth
			if hook {
				h.postAuthPersistHook = func(_ context.Context, auth *coreauth.Auth) error {
					received = auth.Clone()
					return nil
				}
			}
			fresh := target.Clone()
			fresh.Metadata["access_token"] = "new-access"
			fresh.Metadata["headers"] = map[string]any{}
			if savedPath, errSave := h.saveTokenRecord(context.Background(), fresh); errSave != nil || savedPath != path {
				t.Fatalf("save target: path=%q err=%v", savedPath, errSave)
			}
			current, exists := h.authManager.GetByID(target.ID)
			if !exists || current.RegistrationEpoch != target.RegistrationEpoch || current.Attributes["runtime-extension"] != "keep" {
				t.Fatal("adoption changed the target registration or runtime extension")
			}
			if !hook {
				received = current
			}
			if received == nil || received.ID != target.ID || received.ProxyURL != "direct" || received.Metadata["access_token"] != "new-access" || !codexIdentityFieldsEqual(received.Metadata, target.Metadata) {
				t.Fatal("canonical adoption lost the explicit ID, grant, proxy, or namespace")
			}
			if _, duplicate := h.authManager.GetByID("team/account.json"); duplicate {
				t.Fatal("adoption created a second file-derived credential")
			}
			if inFlight, limit := h.authManager.CredentialConcurrencySnapshot(target.ID); inFlight != 1 || limit != 2 {
				t.Fatalf("adoption changed held capacity: %d/%d", inFlight, limit)
			}
		})
	}
}

func TestV851SubjectOnlyXAIAdoptionKeepsRuntimeAndLease(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	h.authManager.SetConfig(h.cfg)
	emailFile := xai.CredentialFileName("account@example.test", "same-subject")
	path := filepath.Join(h.cfg.AuthDir, emailFile)
	target, errRegister := h.authManager.Register(context.Background(), &coreauth.Auth{
		ID: emailFile, FileName: emailFile, Provider: "xai", ProxyURL: "direct",
		Metadata:   map[string]any{"type": "xai", "email": "account@example.test", "sub": "same-subject", "access_token": "old-token", "proxy_url": "direct", "concurrency_limit": 2},
		Attributes: map[string]string{coreauth.AttributePath: path, "runtime-extension": "keep"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	release, errAcquire := h.authManager.AcquireCredentialConcurrency(context.Background(), target)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	defer release()
	subjectFile := xai.CredentialFileName("", "same-subject")
	fresh := &coreauth.Auth{
		ID: subjectFile, FileName: subjectFile, Provider: "xai",
		Storage:  &xai.TokenStorage{AccessToken: "new-token", RefreshToken: "new-refresh", Subject: "same-subject"},
		Metadata: map[string]any{"sub": "same-subject"},
	}
	if savedPath, errSave := h.saveTokenRecord(context.Background(), fresh); errSave != nil || savedPath != path {
		t.Fatalf("adopt email filename: path=%q err=%v", savedPath, errSave)
	}
	current, exists := h.authManager.GetByID(target.ID)
	if !exists || current.RegistrationEpoch != target.RegistrationEpoch || current.Attributes["runtime-extension"] != "keep" || current.ProxyURL != "direct" || current.Metadata["access_token"] != "new-token" {
		t.Fatal("xAI filename adoption replaced the registration, runtime extension, proxy, or grant")
	}
	if inFlight, limit := h.authManager.CredentialConcurrencySnapshot(target.ID); inFlight != 1 || limit != 2 {
		t.Fatalf("xAI filename adoption changed held capacity: %d/%d", inFlight, limit)
	}
}

func TestV851PluginRollbackPreservesReplacementRegistration(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	target := registerCodexOAuthTarget(t, h)
	path := filepath.Join(h.cfg.AuthDir, target.FileName)
	previousBytes, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	h.authManager.Remove(coreauth.WithSkipPersist(context.Background()), target.ID)
	replacement := target.Clone()
	replacement.Metadata["note"] = "replacement-registration"
	replacement.Attributes["runtime-extension"] = "replacement"
	replacement, errRegister := h.authManager.Register(context.Background(), replacement)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	replacementBytes, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	h.rollbackSavedPluginLoginRecords(context.Background(), []savedPluginRecordRollback{{
		savedPath: path, diskPath: path, existedOnDisk: true,
		prevDiskBytes: previousBytes, savedDiskBytes: replacementBytes,
		hadRuntimeAuth: true, hadRecordIDAuth: true, hadFileDerivedAuth: true,
		prevAuth: target, record: replacement.Clone(),
	}})
	current, exists := h.authManager.GetByID(target.ID)
	if !exists || current.RegistrationEpoch != replacement.RegistrationEpoch || current.Attributes["runtime-extension"] != "replacement" {
		t.Fatal("rollback changed a newer registration using the same grant")
	}
	currentBytes, errRead := os.ReadFile(path)
	if errRead != nil || string(currentBytes) != string(replacementBytes) {
		t.Fatal("rollback overwrote the replacement registration's disk record")
	}
}

func TestV851PluginRollbackComparisonPreservesLargeIntegers(t *testing.T) {
	left := []byte(`{"revision":9007199254740992,"nested":{"enabled":true}}`)
	right := []byte(`{"nested":{"enabled":true},"revision":9007199254740993}`)
	if jsonPayloadsEqualOrBytesEqual(left, right) {
		t.Fatal("rollback compared distinct integer revisions as the same snapshot")
	}
	var reformatted any
	decoder := json.NewDecoder(strings.NewReader(string(left)))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&reformatted); errDecode != nil {
		t.Fatal(errDecode)
	}
	encoded, errMarshal := json.MarshalIndent(reformatted, "", "  ")
	if errMarshal != nil || !jsonPayloadsEqualOrBytesEqual(left, encoded) {
		t.Fatalf("equivalent JSON snapshot mismatch: %v", errMarshal)
	}
}

type v851RollbackSerializationStore struct {
	savedJSON []byte
}

func (store *v851RollbackSerializationStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (store *v851RollbackSerializationStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	var errMarshal error
	store.savedJSON, errMarshal = json.Marshal(auth.Metadata)
	return auth.FileName, errMarshal
}

func (store *v851RollbackSerializationStore) Delete(context.Context, string) error {
	return nil
}

func TestV851PluginRollbackNonFileStorePreservesLargeIntegers(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	h.authManager = nil
	store := &v851RollbackSerializationStore{}
	h.tokenStore = store
	path := filepath.Join(h.cfg.AuthDir, "plugin.json")
	previousBytes := []byte(`{"type":"plugin","access_token":"old-token","revision":9007199254740993,"nested":{"revision":18446744073709551615,"history":[9007199254740995]}}`)
	savedBytes := []byte(`{"type":"plugin","access_token":"new-token","revision":9007199254740997}`)
	if errWrite := os.WriteFile(path, savedBytes, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	h.rollbackSavedPluginLoginRecords(context.Background(), []savedPluginRecordRollback{{
		savedPath: path, diskPath: path, existedOnDisk: true,
		prevDiskBytes: previousBytes, savedDiskBytes: savedBytes,
		record: &coreauth.Auth{ID: "plugin.json", FileName: "plugin.json", Provider: "plugin"},
	}})
	if !jsonPayloadsEqualOrBytesEqual(store.savedJSON, previousBytes) {
		t.Fatalf("non-file rollback rounded persisted metadata: got %s, want %s", store.savedJSON, previousBytes)
	}
	restoredBytes, errRead := os.ReadFile(path)
	if errRead != nil || string(restoredBytes) != string(previousBytes) {
		t.Fatalf("rollback changed the original disk snapshot: %v", errRead)
	}
}
