package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialconcurrency"
)

func TestMetadataCredentialVersionAndRejectedStateV817(t *testing.T) {
	for _, atomicStore := range []bool{false, true} {
		for _, change := range []string{"controls", "access_token", "refresh_token", "id_token", "api_key", "failed"} {
			t.Run(fmt.Sprintf("atomic=%t/%s", atomicStore, change), func(t *testing.T) {
				manager, store, initial := newMetadataControlsFixture(t, nil)
				manager.markRejectedAccessToken(initial.ID, "old-token")
				initial, _ = manager.GetByID(initial.ID)
				lease, errAcquire := manager.acquireCredentialConcurrency(t.Context(), initial)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				defer lease.Release()
				if change == "failed" {
					store.failOneSave()
				}
				edit := func(auth *Auth) error {
					auth.ProxyURL = "socks5://accepted.invalid:1080"
					auth.Metadata["proxy_url"] = auth.ProxyURL
					auth.Metadata["codex_identity_namespace"] = "accepted-namespace"
					auth.Metadata[credentialconcurrency.Field] = 3
					if change != "controls" && change != "failed" {
						auth.Metadata[change] = "accepted-secret"
					}
					// A custom callback cannot forge manager-owned execution versions
					// or erase a pending rejection through an unrelated operator edit.
					auth.CredentialVersion = 1000
					auth.RejectedAccessToken = ""
					if atomicStore {
						return nil
					}
					_, errSave := store.Save(t.Context(), auth)
					return errSave
				}
				var errEdit error
				if atomicStore {
					_, errEdit = manager.UpdateAndPersistMetadata(t.Context(), initial.ID, edit)
				} else {
					_, errEdit = manager.UpdatePersistedMetadata(t.Context(), initial.ID, edit)
				}
				if (errEdit != nil) != (change == "failed") {
					t.Fatalf("metadata result = %v, change=%s", errEdit, change)
				}
				current, _ := manager.GetByID(initial.ID)
				wantVersion, wantRejected, wantLimit := initial.CredentialVersion, "old-token", 3
				if change != "controls" && change != "failed" {
					wantVersion++
					wantRejected = ""
				} else if change == "failed" {
					wantLimit = 10
				}
				if current.CredentialVersion != wantVersion || current.RegistrationEpoch != initial.RegistrationEpoch || current.RejectedAccessToken != wantRejected {
					t.Fatal("metadata transaction forged, lost, or failed to advance credential ownership")
				}
				assertCredentialConcurrencyLifecycleSnapshot(t, manager, initial.ID, 1, wantLimit)
				if change != "failed" && (current.ProxyURL != "socks5://accepted.invalid:1080" || current.Metadata["codex_identity_namespace"] != "accepted-namespace") {
					t.Fatal("metadata transaction lost accepted proxy or stable identity")
				}
				if change != "controls" && change != "failed" {
					oldResult := Result{AuthID: initial.ID, Provider: "codex", Model: "versioned-model", CredentialVersion: initial.CredentialVersion, RegistrationEpoch: initial.RegistrationEpoch, Error: &Error{HTTPStatus: 429}}
					manager.MarkResult(t.Context(), oldResult)
					current, _ = manager.GetByID(initial.ID)
					if current.ModelStates["versioned-model"] != nil {
						t.Fatal("old execution result cooled down newly adopted metadata credentials")
					}
					oldResult.CredentialVersion = current.CredentialVersion
					manager.MarkResult(t.Context(), oldResult)
					current, _ = manager.GetByID(initial.ID)
					if current.ModelStates["versioned-model"] == nil || !current.ModelStates["versioned-model"].Quota.Exceeded {
						t.Fatal("current-version result lost its legitimate cooldown")
					}
				}
			})
		}
	}
}

type snapshotRefreshV817Executor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
	err     error
}

func (e *snapshotRefreshV817Executor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	if e.err != nil {
		return nil, e.err
	}
	auth.Metadata["access_token"] = "stale-refreshed-token"
	return auth, nil
}

func TestStaleRefreshOutcomeCannotMutateAcceptedCredentialV817(t *testing.T) {
	for _, replace := range []string{"native-update", "reregister", "reregister-same-token", "atomic-metadata", "identity-transaction"} {
		for _, outcome := range []string{"success", "cancel", "unauthorized", "invalid-grant", "transient"} {
			t.Run(replace+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					manager, store, initial := newMetadataControlsFixture(t, nil)
					executor := &snapshotRefreshV817Executor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}, started: make(chan struct{}), release: make(chan struct{})}
					switch outcome {
					case "cancel":
						executor.err = context.Canceled
					case "unauthorized":
						executor.err = &Error{HTTPStatus: http.StatusUnauthorized, Message: "stale unauthorized"}
					case "invalid-grant":
						executor.err = errors.New("synthetic invalid_grant")
					case "transient":
						executor.err = errors.New("synthetic upstream unavailable")
					}
					manager.RegisterExecutor(executor)
					var once sync.Once
					release := func() { once.Do(func() { close(executor.release) }) }
					defer release()
					lease, errAcquire := manager.acquireCredentialConcurrency(t.Context(), initial)
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					defer lease.Release()
					done := make(chan error, 1)
					go func() {
						_, errRefresh := manager.refreshAuthForRequest(context.Background(), initial.ID, "old-token")
						done <- errRefresh
					}()
					<-executor.started
					edit := func(auth *Auth) error {
						auth.Metadata["access_token"] = "accepted-token"
						auth.Metadata["refresh_token"] = "accepted-refresh"
						auth.Metadata["codex_identity_namespace"] = "accepted-namespace"
						auth.Metadata[credentialconcurrency.Field] = 3
						auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
						auth.ProxyURL = "socks5://accepted.invalid:1080"
						return nil
					}
					candidate, _ := manager.GetByID(initial.ID)
					var errUpdate error
					switch replace {
					case "native-update":
						_ = edit(candidate)
						_, errUpdate = manager.Update(t.Context(), candidate)
					case "reregister", "reregister-same-token":
						_ = edit(candidate)
						if replace == "reregister-same-token" {
							candidate.Metadata["access_token"] = "old-token"
							delete(candidate.Metadata, "refresh_token")
						}
						manager.Remove(t.Context(), initial.ID)
						_, errUpdate = manager.Register(t.Context(), candidate)
					case "atomic-metadata":
						_, errUpdate = manager.UpdateAndPersistMetadata(t.Context(), initial.ID, edit)
					case "identity-transaction":
						_, errUpdate = manager.UpdatePersistedMetadata(t.Context(), initial.ID, func(auth *Auth) error {
							_ = edit(auth)
							_, errSave := store.Save(t.Context(), auth)
							return errSave
						})
					}
					if errUpdate != nil {
						t.Fatal(errUpdate)
					}
					accepted, _ := manager.GetByID(initial.ID)
					release()
					<-done
					current, _ := manager.GetByID(initial.ID)
					if current.CredentialVersion != accepted.CredentialVersion || current.RegistrationEpoch != accepted.RegistrationEpoch || current.Generation != accepted.Generation || current.LastError != accepted.LastError || !current.NextRefreshAfter.Equal(accepted.NextRefreshAfter) || current.RefreshFailures != accepted.RefreshFailures || current.Unavailable != accepted.Unavailable {
						t.Fatal("old refresh completion altered accepted credential availability, version or retry state")
					}
					persisted, errList := store.List(t.Context())
					if errList != nil {
						t.Fatal(errList)
					}
					for _, auth := range append(persisted, current) {
						wantAccess, wantRefresh := any("accepted-token"), any("accepted-refresh")
						if replace == "reregister-same-token" {
							wantAccess, wantRefresh = "old-token", nil
						}
						if auth.Metadata["access_token"] != wantAccess || auth.Metadata["refresh_token"] != wantRefresh || auth.Metadata["codex_identity_namespace"] != "accepted-namespace" || auth.ProxyURL != "socks5://accepted.invalid:1080" {
							t.Fatal("old refresh completion replaced accepted tokens, namespace or proxy")
						}
					}
					assertCredentialConcurrencyLifecycleSnapshot(t, manager, initial.ID, 1, 3)
				})
			})
		}
	}
}

func TestMetadataSecretAdoptionPreservesActiveQuotaV817(t *testing.T) {
	manager, _, initial := newMetadataControlsFixture(t, nil)
	now := time.Now()
	initial.Unavailable, initial.Status = true, StatusError
	initial.NextRetryAfter = now.Add(time.Hour)
	initial.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: initial.NextRetryAfter}
	initial.ModelStates = map[string]*ModelState{"model": {
		Unavailable: true, Status: StatusError, NextRetryAfter: initial.NextRetryAfter,
		Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: initial.NextRetryAfter},
		LastError: &Error{HTTPStatus: 401}, StatusMessage: "unauthorized",
	}}
	if _, errUpdate := manager.Update(t.Context(), initial); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	updated, errUpdate := manager.UpdateAndPersistMetadata(t.Context(), initial.ID, func(auth *Auth) error {
		auth.Metadata["access_token"] = "adopted-token"
		return nil
	})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	state := updated.ModelStates["model"]
	if !updated.Unavailable || !updated.NextRetryAfter.Equal(initial.NextRetryAfter) || !updated.Quota.Exceeded || state == nil || !state.Quota.Exceeded || !state.NextRetryAfter.Equal(initial.NextRetryAfter) || state.LastError != nil {
		t.Fatal("accepted token must clear obsolete 401 while retaining active account/model quota")
	}
}

func TestRefreshFailureWaitsForMetadataSecretPublicationV817(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &metadataStoreV815{memoryAuthTestStore: newMemoryAuthTestStore(), entered: make(chan struct{}), release: make(chan struct{})}
		manager := NewManager(store, nil, nil)
		store.manager = manager
		base, errRegister := manager.Register(t.Context(), &Auth{ID: "pending-secret", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "old-token"}})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		executor := &snapshotRefreshV817Executor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}, started: make(chan struct{}), release: make(chan struct{}), err: errors.New("synthetic old refresh error")}
		manager.RegisterExecutor(executor)
		refreshDone := make(chan error, 1)
		go func() {
			_, errRefresh := manager.ForceRefreshAuth(context.Background(), base.ID)
			refreshDone <- errRefresh
		}()
		<-executor.started
		metadataDone := make(chan error, 1)
		go func() {
			_, errEdit := manager.UpdateAndPersistMetadata(context.Background(), base.ID, func(auth *Auth) error {
				auth.Metadata["access_token"] = "accepted-token"
				auth.Metadata["pause_metadata"] = true
				return nil
			})
			metadataDone <- errEdit
		}()
		<-store.entered
		close(executor.release)
		synctest.Wait()
		select {
		case <-refreshDone:
			t.Error("refresh failure crossed an uncommitted replacement transaction")
		default:
		}
		close(store.release)
		if errEdit := <-metadataDone; errEdit != nil {
			t.Fatal(errEdit)
		}
		<-refreshDone
		current, _ := manager.GetByID(base.ID)
		if current.Metadata["access_token"] != "accepted-token" || current.CredentialVersion != base.CredentialVersion+1 || current.LastError != nil || !current.NextRefreshAfter.IsZero() || current.Unavailable {
			t.Fatal("old refresh failure polluted new credentials while their store save was in progress")
		}
	})
}
