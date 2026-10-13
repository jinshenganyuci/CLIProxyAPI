package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

type codexControlStoreV827 struct{ path string }

func (s *codexControlStoreV827) List(context.Context) ([]*Auth, error) { return nil, nil }
func (s *codexControlStoreV827) Delete(context.Context, string) error  { return nil }
func (s *codexControlStoreV827) Save(ctx context.Context, a *Auth) (string, error) {
	_, err := PersistCodexAuthFile(ctx, s.path, a)
	return s.path, err
}

func TestV827CodexMetadataTransactionsUseOriginalLockAndKeepSIWCIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	store := &codexControlStoreV827{path: path}
	manager := NewManager(store, nil, nil)
	metadata := map[string]any{"type": "codex", "access_token": "outer-old", "refresh_token": "outer-refresh", "use_siwc": true, "siwc": map[string]any{"access_token": "siwc-old", "refresh_token": "siwc-refresh", "client_id": "siwc-client"}, codex.CredentialIdentityVersionMetadataKey: 1, codex.CredentialIdentityNamespaceMetadataKey: "original-namespace"}
	original, err := manager.Register(ctx, &Auth{ID: "codex.json", FileName: "codex.json", Provider: "codex", Status: StatusActive, Metadata: metadata, Storage: &codex.CodexTokenStorage{AccessToken: "outer-old", RefreshToken: "outer-refresh", Metadata: metadata}})
	if err != nil {
		t.Fatal(err)
	}
	release, err := manager.AcquireCredentialConcurrency(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, limit := range []int{3, 2, 10} {
		got, errEdit := manager.UpdateAndPersistMetadata(ctx, original.ID, func(a *Auth) error {
			a.ProxyURL = "direct"
			a.Metadata["proxy_url"] = "direct"
			a.Metadata["concurrency_limit"] = limit
			a.Metadata[codex.CredentialIdentityNamespaceMetadataKey] = "must-not-rotate"
			return nil
		})
		if errEdit != nil {
			t.Fatal(errEdit)
		}
		if got.Metadata[codex.CredentialIdentityNamespaceMetadataKey] != "original-namespace" || got.Metadata["access_token"] != "outer-old" || authSIWCTokenField(got, "access_token") != "siwc-old" || !IsCodexUseSIWC(got) {
			t.Fatal("metadata transaction changed durable identity or credential partition")
		}
		if got.CredentialVersion != original.CredentialVersion {
			t.Fatal("neutral control edit changed credential version")
		}
		if n, cap := manager.CredentialConcurrencySnapshot(got.ID); n != 1 || cap != limit {
			t.Fatalf("transaction changed lease: %d/%d", n, cap)
		}
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		var disk map[string]any
		if errJSON := json.Unmarshal(raw, &disk); errJSON != nil {
			t.Fatal(errJSON)
		}
		if disk["proxy_url"] != "direct" || disk[codex.CredentialIdentityNamespaceMetadataKey] != "original-namespace" || int(disk["concurrency_limit"].(float64)) != limit {
			t.Fatal("disk controls differ from runtime")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "codex.json" && entry.Name() != "codex.json.lock" {
			t.Errorf("metadata save locked or retained a staging path: %s", entry.Name())
		}
	}
	if _, errStat := os.Stat(path + ".lock"); errStat != nil {
		t.Fatal("original credential lock missing")
	}
	release()
	if n, _ := manager.CredentialConcurrencySnapshot(original.ID); n != 0 {
		t.Fatal("lease leaked")
	}
}

func TestV827SIWCRefreshPartitionRespectsRegistrationEpochAndControls(t *testing.T) {
	for _, reregister := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_epoch", true: "new_epoch"}[reregister], func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(newMemoryAuthTestStore(), nil, nil)
			before, err := manager.Register(ctx, &Auth{ID: "split-refresh", Provider: "codex", Status: StatusActive, ProxyURL: "direct", Metadata: map[string]any{"access_token": "outer-old", "refresh_token": "outer-refresh", "use_siwc": true, "siwc": map[string]any{"access_token": "siwc-old", "refresh_token": "siwc-refresh", "client_id": "client"}, "concurrency_limit": 3, codex.CredentialIdentityNamespaceMetadataKey: "stable-namespace"}})
			if err != nil {
				t.Fatal(err)
			}
			release, err := manager.AcquireCredentialConcurrency(ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			refreshed := before.Clone()
			refreshed.Metadata["access_token"] = "outer-refreshed"
			refreshed.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-refreshed"
			refreshed.CodexRefreshPatch = codex.NewCredentialRefreshPatch(before.Metadata, refreshed.Metadata, true, true)
			edited := before.Clone()
			edited.Metadata["access_token"] = "outer-concurrently-replaced"
			edited.Metadata["concurrency_limit"] = 2
			edited.ProxyURL = "http://selected.invalid:8123"
			edited.Metadata["proxy_url"] = edited.ProxyURL
			var current *Auth
			if reregister {
				manager.Remove(ctx, before.ID)
				current, err = manager.Register(ctx, edited)
			} else {
				current, err = manager.Update(ctx, edited)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, errRefresh := manager.UpdateRefreshedAuth(ctx, before, refreshed)
			if reregister {
				if errRefresh == nil {
					t.Fatal("old registration refresh accepted")
				}
				got, _ = manager.GetByID(before.ID)
			} else if errRefresh != nil {
				t.Fatal(errRefresh)
			}
			wantSIWC := "siwc-refreshed"
			wantVersion := current.CredentialVersion + 1
			if reregister {
				wantSIWC = "siwc-old"
				wantVersion = current.CredentialVersion
			}
			if got.Metadata["access_token"] != "outer-concurrently-replaced" || authSIWCTokenField(got, "access_token") != wantSIWC || got.CredentialVersion != wantVersion || got.RegistrationEpoch != current.RegistrationEpoch {
				t.Fatalf("refresh crossed credential fence: outer=%v siwc=%s version=%d epoch=%d", got.Metadata["access_token"], authSIWCTokenField(got, "access_token"), got.CredentialVersion, got.RegistrationEpoch)
			}
			if got.ProxyURL != edited.ProxyURL || got.Metadata[codex.CredentialIdentityNamespaceMetadataKey] != "stable-namespace" {
				t.Fatal("refresh changed credential controls")
			}
			if n, limit := manager.CredentialConcurrencySnapshot(got.ID); n != 1 || limit != 2 {
				t.Fatalf("refresh changed lease %d/%d", n, limit)
			}
			release()
			if n, _ := manager.CredentialConcurrencySnapshot(got.ID); n != 0 {
				t.Fatal("old lease was not released")
			}
		})
	}
}

func TestV827LegacyCodexRefreshUsesPartitionPatchWithoutWeakeningFences(t *testing.T) {
	for _, tc := range []struct {
		name                                        string
		siwc, replaceOuter, replaceSIWC, reregister bool
	}{
		{name: "outer_only"}, {name: "siwc_only", siwc: true}, {name: "other_partition_replaced", replaceSIWC: true}, {name: "same_partition_replaced", replaceOuter: true}, {name: "registration_replaced", reregister: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "legacy.json")
			m := NewManager(&codexControlStoreV827{path: path}, nil, nil)
			before, err := m.Register(ctx, &Auth{ID: "legacy.json", FileName: "legacy.json", Provider: "codex", Status: StatusActive, ProxyURL: "direct", Metadata: map[string]any{"type": "codex", "access_token": "outer-before", "refresh_token": "outer-refresh", "use_siwc": true, "siwc": map[string]any{"access_token": "siwc-before", "refresh_token": "siwc-refresh", "client_id": "client"}, "concurrency_limit": 3, codex.CredentialIdentityNamespaceMetadataKey: "fixed-namespace"}})
			if err != nil {
				t.Fatal(err)
			}
			release, err := m.AcquireCredentialConcurrency(ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			refreshed := before.Clone()
			wantOuter, wantSIWC := "outer-refreshed", "siwc-before"
			if tc.siwc {
				refreshed.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-refreshed"
				wantOuter, wantSIWC = "outer-before", "siwc-refreshed"
			} else {
				refreshed.Metadata["access_token"] = "outer-refreshed"
			}
			current := before
			if tc.replaceOuter || tc.replaceSIWC || tc.reregister {
				replacement := before.Clone()
				replacement.ProxyURL = "http://selected.invalid:8197"
				replacement.Metadata["proxy_url"] = replacement.ProxyURL
				replacement.Metadata["concurrency_limit"] = 2
				if tc.replaceOuter {
					replacement.Metadata["access_token"] = "outer-replaced"
					wantOuter = "outer-replaced"
				}
				if tc.replaceSIWC {
					replacement.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-replaced"
					wantSIWC = "siwc-replaced"
				}
				if tc.reregister {
					m.Remove(ctx, before.ID)
					current, err = m.Register(WithAuthCreationIntent(ctx), replacement)
					wantOuter = "outer-before"
				} else {
					current, err = m.Update(WithAuthCreationIntent(ctx), replacement)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			accepted, errRefresh := m.UpdateRefreshedAuth(ctx, before, refreshed)
			if tc.reregister {
				if errRefresh == nil {
					t.Fatal("legacy refresh crossed registration epoch")
				}
				accepted, _ = m.GetByID(before.ID)
			} else if errRefresh != nil {
				t.Fatal(errRefresh)
			}
			if refreshed.CodexRefreshPatch != nil {
				t.Fatal("compatibility adapter mutated caller refresh object")
			}
			expectedVersion := current.CredentialVersion
			if !tc.replaceOuter && !tc.reregister {
				expectedVersion++
			}
			if accepted.Metadata["access_token"] != wantOuter || authSIWCTokenField(accepted, "access_token") != wantSIWC || accepted.CredentialVersion != expectedVersion || accepted.RegistrationEpoch != current.RegistrationEpoch {
				t.Fatalf("legacy refresh lost partition/version fence: outer=%v siwc=%s version=%d epoch=%d", accepted.Metadata["access_token"], authSIWCTokenField(accepted, "access_token"), accepted.CredentialVersion, accepted.RegistrationEpoch)
			}
			if accepted.ProxyURL != current.ProxyURL || accepted.Metadata[codex.CredentialIdentityNamespaceMetadataKey] != "fixed-namespace" {
				t.Fatal("legacy refresh changed proxy or identity")
			}
			raw, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			var disk map[string]any
			if errJSON := json.Unmarshal(raw, &disk); errJSON != nil {
				t.Fatal(errJSON)
			}
			if disk["access_token"] != wantOuter || disk["siwc"].(map[string]any)["access_token"] != wantSIWC {
				t.Fatal("legacy refresh did not durably publish accepted partitions")
			}
			if n, limit := m.CredentialConcurrencySnapshot(before.ID); n != 1 || limit != EffectiveConcurrencyLimit(current) {
				t.Fatalf("legacy refresh changed active lease=%d/%d", n, limit)
			}
			release()
			if n, _ := m.CredentialConcurrencySnapshot(before.ID); n != 0 {
				t.Fatal("legacy refresh lease did not release")
			}
		})
	}
}

func TestV827LegacyRefreshPersistsThreeWayControls(t *testing.T) {
	for _, tc := range []struct {
		name                                                 string
		metadataProxy, clearProxy, controlsOnly, runtimeEdit bool
		diskProxy                                            *string
	}{
		{name: "struct_proxy"},
		{name: "metadata_proxy", metadataProxy: true},
		{name: "clear_proxy", clearProxy: true},
		{name: "controls_only", controlsOnly: true},
		{name: "runtime_management_wins", runtimeEdit: true},
		{name: "disk_direct_wins", diskProxy: stringPointerV827("direct")},
		{name: "disk_clear_wins", diskProxy: stringPointerV827("")},
		{name: "disk_invalid_proxy_remains_explicit", diskProxy: stringPointerV827("://invalid")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "controls.json")
			m := NewManager(&codexControlStoreV827{path: path}, nil, nil)
			before, err := m.Register(ctx, &Auth{ID: "controls.json", Provider: "codex", Status: StatusActive,
				ProxyURL: "http://old.invalid:8123", Attributes: map[string]string{"concurrency_limit": "3", AttributeWeight: "3", "priority": "3"},
				Metadata: map[string]any{"type": "codex", "proxy_url": "http://old.invalid:8123", "access_token": "outer-old", "refresh_token": "outer-refresh",
					"use_siwc": true, "siwc": map[string]any{"access_token": "siwc-old", "refresh_token": "siwc-refresh", "client_id": "client"},
					"concurrency_limit": 3, "weight": 3, "priority": 3, "notes": "old-note", "delete_me": "old", "preserve_if_changed": "old", "nullable": nil,
					codex.CredentialIdentityVersionMetadataKey: 1, codex.CredentialIdentityNamespaceMetadataKey: "stable-namespace"}})
			if err != nil {
				t.Fatal(err)
			}
			release, err := m.AcquireCredentialConcurrency(ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			updated := before.Clone()
			wantToken := "outer-new"
			if tc.controlsOnly {
				wantToken = "outer-old"
			} else {
				updated.Metadata["access_token"] = wantToken
			}
			wantProxy := "http://new.invalid:8124"
			if tc.clearProxy {
				wantProxy = ""
			}
			if tc.metadataProxy {
				updated.Metadata["proxy_url"] = wantProxy
			} else {
				updated.ProxyURL = wantProxy
			}
			updated.Metadata["notes"] = "refreshed-note"
			updated.Metadata["weight"] = 4
			updated.Metadata["priority"] = 4
			updated.Metadata["nullable"] = "filled"
			delete(updated.Metadata, "delete_me")
			delete(updated.Metadata, "preserve_if_changed")
			updated.Metadata["concurrency_limit"] = 7 // Refreshers never change scheduler capacity.
			updated.Metadata[codex.CredentialIdentityNamespaceMetadataKey] = "must-not-rotate"
			wantLimit, wantWeight, wantNote := 3, 4, "refreshed-note"
			if tc.runtimeEdit {
				_, err = m.UpdateAndPersistMetadata(ctx, before.ID, func(a *Auth) error {
					a.ProxyURL = "direct"
					a.Metadata["proxy_url"] = "direct"
					a.Metadata["concurrency_limit"] = 2
					a.Metadata["notes"] = "management-note"
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				wantProxy, wantLimit, wantNote = "direct", 2, "management-note"
			}
			if tc.diskProxy != nil {
				err = codex.WithFileLock(path, func() error {
					raw, errRead := os.ReadFile(path)
					if errRead != nil {
						return errRead
					}
					var disk map[string]any
					if errJSON := json.Unmarshal(raw, &disk); errJSON != nil {
						return errJSON
					}
					disk["proxy_url"] = *tc.diskProxy
					disk["concurrency_limit"] = 2
					disk["weight"] = 5
					disk["priority"] = 5
					disk["notes"] = "disk-note"
					disk["preserve_if_changed"] = "disk-new"
					disk["nullable"] = "disk-nonnull"
					disk["siwc"].(map[string]any)["access_token"] = "siwc-newer"
					return codex.WriteJSONFileAtomic(path, disk)
				})
				if err != nil {
					t.Fatal(err)
				}
				wantProxy, wantLimit, wantWeight, wantNote = *tc.diskProxy, 2, 5, "disk-note"
			}
			got, err := m.UpdateRefreshedAuth(ctx, before, updated)
			if err != nil {
				t.Fatal(err)
			}
			if got.ProxyURL != wantProxy {
				t.Fatalf("runtime proxy = %q, want %q", got.ProxyURL, wantProxy)
			}
			if proxy, _ := got.Metadata["proxy_url"].(string); proxy != wantProxy {
				t.Fatalf("metadata proxy = %q, want %q", proxy, wantProxy)
			}
			if got.Metadata["notes"] != wantNote || got.Metadata["access_token"] != wantToken || got.Metadata[codex.CredentialIdentityNamespaceMetadataKey] != "stable-namespace" {
				t.Fatal("refresh lost controls, credential partition, or stable identity")
			}
			if _, exists := got.Metadata["delete_me"]; exists {
				t.Fatal("accepted metadata deletion was lost")
			}
			wantSIWC := "siwc-old"
			if tc.diskProxy != nil {
				wantSIWC = "siwc-newer"
				if got.Metadata["preserve_if_changed"] != "disk-new" || got.Metadata["nullable"] != "disk-nonnull" {
					t.Fatal("refresh overwrote newer disk metadata")
				}
			} else {
				if _, exists := got.Metadata["preserve_if_changed"]; exists {
					t.Fatal("metadata deletion was lost")
				}
				if got.Metadata["nullable"] != "filled" {
					t.Fatal("null-to-value metadata update was lost")
				}
			}
			if authSIWCTokenField(got, "access_token") != wantSIWC || !IsCodexUseSIWC(got) {
				t.Fatal("controls crossed SIWC partition")
			}
			if got.Attributes["priority"] != fmt.Sprint(wantWeight) {
				t.Fatalf("priority mirror = %v", got.Attributes["priority"])
			}
			if got.Attributes[AttributeWeight] != fmt.Sprint(wantWeight) {
				t.Fatalf("weight mirror = %v", got.Attributes[AttributeWeight])
			}
			if n, limit := m.CredentialConcurrencySnapshot(got.ID); n != 1 || limit != wantLimit {
				t.Fatalf("active lease = %d/%d, want 1/%d", n, limit, wantLimit)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var disk map[string]any
			if err := json.Unmarshal(raw, &disk); err != nil {
				t.Fatal(err)
			}
			metadataJSON, err := json.Marshal(got.Metadata)
			if err != nil {
				t.Fatal(err)
			}
			var runtimeMap map[string]any
			if err := json.Unmarshal(metadataJSON, &runtimeMap); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runtimeMap, disk) {
				t.Fatal("restart would restore different metadata")
			}
			release()
			if n, _ := m.CredentialConcurrencySnapshot(got.ID); n != 0 {
				t.Fatal("lease leaked")
			}
			if updated.CodexRefreshPatch != nil || updated.Metadata[codex.CredentialIdentityNamespaceMetadataKey] != "must-not-rotate" {
				t.Fatal("caller snapshot was mutated")
			}
		})
	}
}

func stringPointerV827(value string) *string { return &value }

func TestV827RefreshDiskReplacementAndDisabledFence(t *testing.T) {
	for _, tc := range []struct {
		name                                                                                   string
		replaceTokens, controlsOnly, initiallyDisabled, diskDisabled, removeDisabled, cooldown bool
	}{
		{name: "replacement_rejects_refresh_controls", replaceTokens: true},
		{name: "replacement_rejects_control_only_refresh", replaceTokens: true, controlsOnly: true},
		{name: "disk_reenabled", initiallyDisabled: true},
		{name: "disk_disabled_flag_removed", initiallyDisabled: true, removeDisabled: true},
		{name: "disk_disabled", diskDisabled: true},
		{name: "disk_enabled_retains_runtime_failure", cooldown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "fence.json")
			m := NewManager(&codexControlStoreV827{path: path}, nil, nil)
			status := StatusActive
			if tc.initiallyDisabled {
				status = StatusDisabled
			}
			a := &Auth{ID: "fence.json", Provider: "codex", ProxyURL: "direct", Status: status, Disabled: tc.initiallyDisabled,
				Metadata: map[string]any{"type": "codex", "proxy_url": "direct", "access_token": "old-outer", "refresh_token": "old-refresh", "use_siwc": true,
					"siwc": map[string]any{"access_token": "old-siwc", "refresh_token": "old-siwc-refresh", "client_id": "client"}, "notes": "old-note", "concurrency_limit": 3}}
			if tc.cooldown {
				a.Status, a.Unavailable = StatusError, true
				a.LastError = &Error{HTTPStatus: 503, Message: "newer-runtime-failure"}
				a.NextRetryAfter = time.Now().Add(time.Hour)
				a.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour)}
			}
			before, err := m.Register(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			updated := before.Clone()
			updated.ProxyURL = "http://stale-refresh.invalid:8123"
			updated.Metadata["notes"] = "stale-refresh-note"
			if !tc.controlsOnly {
				updated.Metadata["access_token"] = "refreshed-outer"
			}
			err = codex.WithFileLock(path, func() error {
				raw, errRead := os.ReadFile(path)
				if errRead != nil {
					return errRead
				}
				var disk map[string]any
				if errJSON := json.Unmarshal(raw, &disk); errJSON != nil {
					return errJSON
				}
				disk["disabled"] = tc.diskDisabled
				if tc.removeDisabled {
					delete(disk, "disabled")
				}
				if tc.replaceTokens {
					disk["access_token"], disk["refresh_token"] = "replaced-outer", "replaced-refresh"
					disk["siwc"].(map[string]any)["access_token"] = "replaced-siwc"
					disk["siwc"].(map[string]any)["refresh_token"] = "replaced-siwc-refresh"
				}
				return codex.WriteJSONFileAtomic(path, disk)
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := m.UpdateRefreshedAuth(ctx, before, updated)
			if err != nil {
				t.Fatal(err)
			}
			if tc.replaceTokens && (got.ProxyURL != "direct" || got.Metadata["notes"] != "old-note" || got.Metadata["access_token"] != "replaced-outer" || authSIWCTokenField(got, "access_token") != "replaced-siwc") {
				t.Fatal("stale refresh modified the replacement credential")
			}
			wantStatus := StatusActive
			if tc.diskDisabled {
				wantStatus = StatusDisabled
			} else if tc.cooldown {
				wantStatus = StatusError
			}
			if got.Disabled != tc.diskDisabled || got.Status != wantStatus {
				t.Fatalf("disabled/status = %v/%v, want %v/%v", got.Disabled, got.Status, tc.diskDisabled, wantStatus)
			}
			if tc.cooldown && (!got.Unavailable || got.LastError == nil || got.LastError.Message != "newer-runtime-failure" || !got.Quota.Exceeded || !got.NextRetryAfter.Equal(before.NextRetryAfter)) {
				t.Fatal("disk enabled flag cleared runtime failure or active quota")
			}
		})
	}
}

func TestV827CodexNoopPersistencePreservesOriginalBytes(t *testing.T) {
	for _, mode := range []string{"metadata", "refresh_controls", "refresh_patch"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "format.json")
			original := []byte("{\n  \"type\": \"codex\", \"disabled\": false, \"proxy_url\": \"direct\",\n  \"access_token\": \"outer\", \"refresh_token\": \"refresh\",\n  \"priority\": 3.0, \"notes\": \"same\", \"remove\": \"present\", \"nested\": {\"b\": 2, \"a\": 1}\n}\n")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			stamp := time.Unix(1700000000, 0)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			beforeInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			var metadata map[string]any
			if err := json.Unmarshal(original, &metadata); err != nil {
				t.Fatal(err)
			}
			m := NewManager(&codexControlStoreV827{path: path}, nil, nil)
			before, err := m.Register(WithSkipPersist(ctx), &Auth{ID: "format.json", Provider: "codex", Status: StatusActive, ProxyURL: "direct", Metadata: metadata})
			if err != nil {
				t.Fatal(err)
			}
			updated := before.Clone()
			updated.Metadata["priority"] = 3 // Same persisted value with a different Go type.
			apply := func(base, next *Auth) (*Auth, error) {
				if mode == "metadata" {
					return m.UpdatePreparedAuth(ctx, base, next)
				}
				if mode == "refresh_patch" {
					next.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, next.Metadata, true, false)
				}
				return m.UpdateRefreshedAuth(ctx, base, next)
			}
			got, err := apply(before, updated)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			afterInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != string(original) || !os.SameFile(beforeInfo, afterInfo) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
				t.Fatal("semantic no-op rewrote original bytes, inode, or mtime")
			}
			changed := got.Clone()
			changed.Metadata["notes"] = "changed"
			delete(changed.Metadata, "remove")
			wantToken := "outer"
			if mode == "refresh_patch" {
				wantToken = "outer-refreshed"
				changed.Metadata["access_token"] = wantToken
			}
			if _, err := apply(got, changed); err != nil {
				t.Fatal(err)
			}
			raw, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var disk map[string]any
			if err := json.Unmarshal(raw, &disk); err != nil {
				t.Fatal(err)
			}
			if disk["notes"] != "changed" || disk["access_token"] != wantToken {
				t.Fatal("real metadata/token change was not persisted")
			}
			if _, exists := disk["remove"]; exists {
				t.Fatal("real metadata deletion was not persisted")
			}
			changedInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(beforeInfo, changedInfo) || changedInfo.Mode().Perm() != 0o600 {
				t.Fatal("real change did not use the private atomic replacement")
			}
		})
	}
}
