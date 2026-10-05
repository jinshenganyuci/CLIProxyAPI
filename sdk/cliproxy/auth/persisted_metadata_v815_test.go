package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialconcurrency"
)

type metadataStoreV815 struct {
	*memoryAuthTestStore
	manager *Manager
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	fail    bool
}

func (s *metadataStoreV815) Save(ctx context.Context, auth *Auth) (string, error) {
	// Native stores may inspect runtime state without deadlocking a save.
	s.manager.GetByID(auth.ID)
	s.manager.List()
	if auth.Metadata["pause_metadata"] == true {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
		if s.fail {
			return "", errors.New("synthetic atomic metadata save failure")
		}
	}
	auth.FileName = auth.ID + ".json"
	return s.memoryAuthTestStore.Save(ctx, auth)
}

func TestMetadataTransactionUsesNativeMutationAndLoadGatesV815(t *testing.T) {
	for _, atomicStore := range []bool{false, true} {
		for _, concurrent := range []string{"refresh", "load", "failure"} {
			t.Run(fmt.Sprintf("atomic=%t/%s", atomicStore, concurrent), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx := context.Background()
					store := &metadataStoreV815{memoryAuthTestStore: newMemoryAuthTestStore(), entered: make(chan struct{}), release: make(chan struct{}), fail: concurrent == "failure"}
					manager := NewManager(store, nil, nil)
					store.manager = manager
					base, errRegister := manager.Register(ctx, &Auth{ID: "edited", Provider: "codex", Status: StatusActive, ProxyURL: "direct", Metadata: map[string]any{
						"access_token": "old-token", "refresh_token": "kept-refresh", "codex_identity_namespace": "original-namespace",
					}})
					if errRegister != nil {
						t.Fatal(errRegister)
					}
					for range 2 {
						lease, errAcquire := manager.acquireCredentialConcurrency(ctx, base)
						if errAcquire != nil {
							t.Fatal(errAcquire)
						}
						defer lease.Release()
					}
					changed, unwatch := manager.WatchAuthChanges(base.ID)
					defer unwatch()
					edit := func(auth *Auth) error {
						auth.Metadata["pause_metadata"] = true
						auth.Metadata[credentialconcurrency.Field] = 3
						auth.Metadata["codex_identity_namespace"] = "accepted-namespace"
						auth.ProxyURL = "socks5://selected.invalid:1080"
						if atomicStore {
							return nil
						}
						_, errSave := store.Save(ctx, auth)
						return errSave
					}
					done := make(chan error, 1)
					go func() {
						var errEdit error
						if atomicStore {
							_, errEdit = manager.UpdateAndPersistMetadata(ctx, base.ID, edit)
						} else {
							_, errEdit = manager.UpdatePersistedMetadata(ctx, base.ID, edit)
						}
						done <- errEdit
					}()
					<-store.entered
					current, _ := manager.GetByID(base.ID)
					if current.ProxyURL != "direct" || current.Metadata["codex_identity_namespace"] != "original-namespace" {
						t.Fatal("operator edit became visible before durable acceptance")
					}
					assertCredentialConcurrencyLifecycleSnapshot(t, manager, base.ID, 2, 10)
					if _, errOther := manager.Register(ctx, &Auth{ID: "unrelated", Provider: "codex", Metadata: map[string]any{"access_token": "other"}}); errOther != nil {
						t.Fatal(errOther)
					}
					otherDone := make(chan error, 1)
					if concurrent != "failure" {
						go func() {
							if concurrent == "load" {
								otherDone <- manager.Load(ctx)
								return
							}
							fresh := base.Clone()
							fresh.Metadata["access_token"] = "refreshed-token"
							_, errRefresh := manager.UpdateRefreshedAuth(ctx, base, fresh)
							otherDone <- errRefresh
						}()
						synctest.Wait()
						select {
						case <-otherDone:
							t.Fatal("same-auth refresh or store reload overtook an uncommitted metadata transaction")
						default:
						}
					}
					close(store.release)
					errEdit := <-done
					if concurrent == "failure" {
						if errEdit == nil {
							t.Fatal("failed store published metadata")
						}
						assertCredentialConcurrencyLifecycleSnapshot(t, manager, base.ID, 2, 10)
						select {
						case <-changed:
							t.Fatal("failed save notified structural auth watchers")
						default:
						}
					} else {
						if errEdit != nil {
							t.Fatal(errEdit)
						}
						if errOther := <-otherDone; errOther != nil {
							t.Fatal(errOther)
						}
						assertCredentialConcurrencyLifecycleSnapshot(t, manager, base.ID, 2, 3)
						select {
						case <-changed:
						default:
							t.Fatal("committed edit did not notify native probe watchers")
						}
					}
					current, _ = manager.GetByID(base.ID)
					persisted, _ := store.List(ctx)
					for _, candidate := range append(persisted, current) {
						if candidate.ID != base.ID {
							continue
						}
						wantProxy, wantIdentity, wantToken, wantLimit := "socks5://selected.invalid:1080", "accepted-namespace", "old-token", 3
						if concurrent == "failure" {
							wantProxy, wantIdentity, wantLimit = "direct", "original-namespace", 10
						} else if concurrent == "refresh" {
							wantToken = "refreshed-token"
						}
						if candidate.ProxyURL != wantProxy || candidate.Metadata["codex_identity_namespace"] != wantIdentity || candidate.Metadata["access_token"] != wantToken || candidate.Metadata["refresh_token"] != "kept-refresh" || EffectiveConcurrencyLimit(candidate) != wantLimit || candidate.FileName != "edited.json" {
							t.Fatal("metadata/refresh/load lost the selected route, identity, limit, token or store enrichment")
						}
					}
				})
			})
		}
	}
}

func TestMetadataTransactionCancellationWaitAndWatchV815(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, _, auth := newMetadataControlsFixture(t, nil)
		release := manager.lockAuthMutation(auth.ID)
		defer release()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, errUpdate := manager.UpdateAndPersistMetadata(ctx, auth.ID, func(*Auth) error {
				t.Error("canceled metadata edit executed")
				return nil
			})
			done <- errUpdate
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		assertCanceledMutation(t, done)
	})
}
