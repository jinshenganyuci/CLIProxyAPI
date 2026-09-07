package auth

import (
	"context"
	"fmt"
	"testing"
)

func TestUpdatePersistedMetadataFlushesPendingRefresh(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, nil, nil)
	registered, errRegister := manager.Register(ctx, &Auth{ID: "pending", Provider: "codex", Metadata: map[string]any{"access_token": "old", "namespace": "old"}})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	// Stop exactly between the refresh runtime publication and its persist call.
	pending := registered.Clone()
	pending.Metadata["access_token"] = "new"
	pending.Generation++
	manager.mu.Lock()
	manager.auths[pending.ID] = pending.Clone()
	manager.mu.Unlock()
	_, errUpdate := manager.UpdatePersistedMetadata(ctx, pending.ID, func(latest *Auth) error {
		persisted, _ := store.List(ctx)
		if len(persisted) != 1 || persisted[0].Metadata["access_token"] != "new" {
			return fmt.Errorf("pending refresh was not flushed before identity commit")
		}
		latest.Metadata["namespace"] = "rotated"
		_, errSave := store.Save(ctx, latest)
		return errSave
	})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errPersist := manager.persist(ctx, pending); errPersist != nil {
		t.Fatal(errPersist)
	}
	persisted, _ := store.List(ctx)
	if persisted[0].Metadata["access_token"] != "new" || persisted[0].Metadata["namespace"] != "rotated" {
		t.Fatal("deferred refresh persistence replaced the transaction")
	}
}

func TestUpdatePersistedMetadataCoordinatesConcurrentRefresh(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, nil, nil)
	base, errRegister := manager.Register(ctx, &Auth{ID: "concurrent", Provider: "codex", Metadata: map[string]any{"access_token": "old", "namespace": "old"}})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	transactionDone := make(chan error, 1)
	go func() {
		_, errUpdate := manager.UpdatePersistedMetadata(ctx, base.ID, func(latest *Auth) error {
			close(entered)
			<-release
			latest.Metadata["namespace"] = "rotated"
			_, errSave := store.Save(ctx, latest)
			return errSave
		})
		transactionDone <- errUpdate
	}()
	<-entered
	refreshStarted, refreshDone := make(chan struct{}), make(chan error, 1)
	go func() {
		refreshed := base.Clone()
		refreshed.Metadata["access_token"] = "new"
		close(refreshStarted)
		_, errRefresh := manager.UpdateRefreshedAuth(ctx, base, refreshed)
		refreshDone <- errRefresh
	}()
	<-refreshStarted
	close(release)
	if errTransaction := <-transactionDone; errTransaction != nil {
		t.Fatal(errTransaction)
	}
	if errRefresh := <-refreshDone; errRefresh != nil {
		t.Fatal(errRefresh)
	}
	current, _ := manager.GetByID(base.ID)
	persisted, _ := store.List(ctx)
	for _, auth := range []*Auth{current, persisted[0]} {
		if auth.Metadata["access_token"] != "new" || auth.Metadata["namespace"] != "rotated" {
			t.Fatal("concurrent refresh lost a token or reverted the identity")
		}
	}
}

type failingMetadataTransactionStore struct {
	*memoryAuthTestStore
	failNext bool
}

func (s *failingMetadataTransactionStore) Save(ctx context.Context, auth *Auth) (string, error) {
	if s.failNext {
		s.failNext = false
		return "", fmt.Errorf("injected token persistence failure")
	}
	return s.memoryAuthTestStore.Save(ctx, auth)
}

func TestUpdatePersistedMetadataRetriesFailedRefreshPersistence(t *testing.T) {
	ctx := context.Background()
	store := &failingMetadataTransactionStore{memoryAuthTestStore: newMemoryAuthTestStore()}
	manager := NewManager(store, nil, nil)
	base, errRegister := manager.Register(ctx, &Auth{ID: "failed-refresh", Provider: "codex", Metadata: map[string]any{"access_token": "old", "namespace": "old"}})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	refreshed := base.Clone()
	refreshed.Metadata["access_token"] = "new"
	store.failNext = true
	if _, errRefresh := manager.UpdateRefreshedAuth(ctx, base, refreshed); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	persisted, _ := store.List(ctx)
	if persisted[0].Metadata["access_token"] != "old" {
		t.Fatal("injected refresh persistence failure did not leave the old disk token")
	}
	store.failNext = true
	callbackCalled := false
	if _, errUpdate := manager.UpdatePersistedMetadata(ctx, base.ID, func(*Auth) error {
		callbackCalled = true
		return nil
	}); errUpdate == nil || callbackCalled {
		t.Fatal("identity transaction continued after its pending token flush failed")
	}
	_, errUpdate := manager.UpdatePersistedMetadata(ctx, base.ID, func(latest *Auth) error {
		persisted, _ := store.List(ctx)
		if persisted[0].Metadata["access_token"] != "new" {
			return fmt.Errorf("failed refresh write was mistaken for durable persistence")
		}
		latest.Metadata["namespace"] = "rotated"
		_, errSave := store.Save(ctx, latest)
		return errSave
	})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	persisted, _ = store.List(ctx)
	if persisted[0].Metadata["access_token"] != "new" || persisted[0].Metadata["namespace"] != "rotated" {
		t.Fatal("transaction failed to preserve the retried refresh and new identity")
	}
}
