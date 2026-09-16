package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	baseauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialconcurrency"
)

type metadataControlsStorage struct {
	metadata map[string]any
	sets     int
}

func (s *metadataControlsStorage) SaveTokenToFile(string) error { return nil }

func (s *metadataControlsStorage) SetMetadata(metadata map[string]any) {
	s.metadata = metadata
	s.sets++
}

func (s *metadataControlsStorage) CloneForMetadataUpdate() (baseauth.TokenStorage, error) {
	metadata, errClone := baseauth.CloneMetadataForUpdate(s.metadata)
	if errClone != nil {
		return nil, errClone
	}
	return &metadataControlsStorage{metadata: metadata, sets: s.sets}, nil
}

type metadataControlsStore struct {
	*memoryAuthTestStore
	mu       sync.Mutex
	failNext bool
	saves    int
}

func (s *metadataControlsStore) Save(ctx context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if setter, ok := auth.Storage.(interface{ SetMetadata(map[string]any) }); ok {
		setter.SetMetadata(auth.Metadata)
	}
	if s.failNext {
		s.failNext = false
		return "", fmt.Errorf("synthetic metadata persistence failure")
	}
	return s.memoryAuthTestStore.Save(ctx, auth)
}

func (s *metadataControlsStore) failOneSave() {
	s.mu.Lock()
	s.failNext = true
	s.mu.Unlock()
}

func (s *metadataControlsStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func newMetadataControlsFixture(t *testing.T, storage baseauth.TokenStorage) (*Manager, *metadataControlsStore, *Auth) {
	t.Helper()
	store := &metadataControlsStore{memoryAuthTestStore: newMemoryAuthTestStore()}
	manager := NewManager(store, nil, nil)
	registered, errRegister := manager.Register(context.Background(), &Auth{
		ID: t.Name(), Provider: "codex", Status: StatusActive, Storage: storage,
		Attributes: map[string]string{},
		Metadata: map[string]any{
			"access_token": "old-token", "nested": map[string]any{"value": "original"},
			credentialconcurrency.Field: 10,
		},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	return manager, store, registered
}

func TestUpdateAndPersistMetadataFailureKeepsRuntimeAndStorage(t *testing.T) {
	storage := &metadataControlsStorage{}
	manager, store, original := newMetadataControlsFixture(t, storage)
	beforeSets := storage.sets
	beforeGeneration := original.Generation
	store.failOneSave()
	edit := func(latest *Auth) error {
		latest.Metadata[credentialconcurrency.Field] = 3
		latest.Metadata["nested"].(map[string]any)["value"] = "edited"
		latest.Attributes[credentialconcurrency.Field] = "3"
		return nil
	}
	if updated, errUpdate := manager.UpdateAndPersistMetadata(context.Background(), original.ID, edit); errUpdate == nil || updated != nil {
		t.Fatal("failed save published a successful update")
	}
	current, _ := manager.GetByID(original.ID)
	if current.Generation != beforeGeneration || EffectiveConcurrencyLimit(current) != 10 || current.Metadata["nested"].(map[string]any)["value"] != "original" || len(current.Attributes) != 0 {
		t.Fatal("failed save mutated the live credential or nested metadata")
	}
	if storage.sets != beforeSets || storage.metadata["nested"].(map[string]any)["value"] != "original" || storage.metadata[credentialconcurrency.Field] != 10 || current.Storage != storage {
		t.Fatal("failed save mutated or replaced the previously live token storage")
	}
	persisted, _ := store.List(context.Background())
	if len(persisted) != 1 || EffectiveConcurrencyLimit(persisted[0]) != 10 {
		t.Fatal("failed save changed the durable credential")
	}
	updated, errRetry := manager.UpdateAndPersistMetadata(context.Background(), original.ID, edit)
	if errRetry != nil {
		t.Fatalf("retry failed: %v", errRetry)
	}
	if EffectiveConcurrencyLimit(updated) != 3 || updated.Generation != beforeGeneration+1 || updated.Storage == storage {
		t.Fatal("successful retry did not publish the isolated update")
	}
	if storage.sets != beforeSets || storage.metadata["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("successful retry modified storage held by an old credential snapshot")
	}
	persisted, _ = store.List(context.Background())
	if len(persisted) != 1 || EffectiveConcurrencyLimit(persisted[0]) != 3 || persisted[0].Metadata["access_token"] != "old-token" {
		t.Fatal("successful retry did not durably retain the latest token and limit")
	}
}

func TestUpdateAndPersistMetadataPendingFlushIsolatesStorage(t *testing.T) {
	storage := &metadataControlsStorage{}
	manager, store, original := newMetadataControlsFixture(t, storage)
	pending := original.Clone()
	pending.Generation++
	pending.Metadata["access_token"] = "new-token"
	manager.mu.Lock()
	manager.auths[pending.ID] = pending.Clone()
	manager.mu.Unlock()
	beforeSets := storage.sets
	store.failOneSave()
	edited := false
	edit := func(latest *Auth) error {
		edited = true
		if latest.Metadata["access_token"] != "new-token" {
			return fmt.Errorf("editor received a stale token")
		}
		latest.Metadata[credentialconcurrency.Field] = 3
		return nil
	}
	if _, errUpdate := manager.UpdateAndPersistMetadata(context.Background(), pending.ID, edit); errUpdate == nil || edited {
		t.Fatal("editor ran before the pending refresh was durably flushed")
	}
	if storage.sets != beforeSets || storage.metadata["access_token"] != "old-token" {
		t.Fatal("failed pending flush mutated the live storage object")
	}
	if _, errRetry := manager.UpdateAndPersistMetadata(context.Background(), pending.ID, edit); errRetry != nil {
		t.Fatalf("retry pending flush: %v", errRetry)
	}
	if errPersist := manager.persist(context.Background(), pending); errPersist != nil {
		t.Fatal(errPersist)
	}
	current, _ := manager.GetByID(pending.ID)
	persisted, _ := store.List(context.Background())
	for _, auth := range []*Auth{current, persisted[0]} {
		if auth.Metadata["access_token"] != "new-token" || EffectiveConcurrencyLimit(auth) != 3 {
			t.Fatal("delayed refresh reverted the saved operator limit or token")
		}
	}
}

func TestUpdateAndPersistMetadataPreservesConcurrentRefresh(t *testing.T) {
	manager, store, original := newMetadataControlsFixture(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	transactionDone := make(chan error, 1)
	go func() {
		_, errUpdate := manager.UpdateAndPersistMetadata(context.Background(), original.ID, func(latest *Auth) error {
			close(entered)
			<-release
			latest.Metadata[credentialconcurrency.Field] = 3
			return nil
		})
		transactionDone <- errUpdate
	}()
	<-entered
	refreshStarted, refreshDone := make(chan struct{}), make(chan error, 1)
	go func() {
		refreshed := original.Clone()
		refreshed.Metadata["access_token"] = "new-token"
		close(refreshStarted)
		_, errRefresh := manager.UpdateRefreshedAuth(context.Background(), original, refreshed)
		refreshDone <- errRefresh
	}()
	<-refreshStarted
	close(release)
	if errUpdate := <-transactionDone; errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errRefresh := <-refreshDone; errRefresh != nil {
		t.Fatal(errRefresh)
	}
	current, _ := manager.GetByID(original.ID)
	persisted, _ := store.List(context.Background())
	for _, auth := range []*Auth{current, persisted[0]} {
		if auth.Metadata["access_token"] != "new-token" || EffectiveConcurrencyLimit(auth) != 3 {
			t.Fatal("concurrent refresh lost the updated limit or fresh token")
		}
	}
}

func TestUpdateAndPersistMetadataRejectsUnsupportedSourcesBeforeWriting(t *testing.T) {
	for _, test := range []struct {
		name       string
		attributes map[string]string
		noStore    bool
		storage    baseauth.TokenStorage
	}{
		{name: "missing store", noStore: true},
		{name: "config key", attributes: map[string]string{AttributeAPIKey: "synthetic", AttributeSource: "config:codex"}},
		{name: "runtime only", attributes: map[string]string{AttributeRuntimeOnly: " true "}},
		{name: "plugin virtual", attributes: map[string]string{AttributePluginVirtual: "true"}},
		{name: "unknown storage", storage: &dummyMetadataStorage{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, original := newMetadataControlsFixture(t, nil)
			manager.mu.Lock()
			manager.auths[original.ID].Attributes = test.attributes
			manager.auths[original.ID].Storage = test.storage
			manager.auths[original.ID].Generation++
			manager.mu.Unlock()
			if test.noStore {
				manager.SetStore(nil)
			}
			before := store.saveCount()
			edited := false
			if _, errUpdate := manager.UpdateAndPersistMetadata(context.Background(), original.ID, func(*Auth) error { edited = true; return nil }); errUpdate == nil || edited {
				t.Fatal("unsupported credential accepted an operator edit")
			}
			if store.saveCount() != before {
				t.Fatal("unsupported credential was written during pending flush")
			}
		})
	}
}

func TestUpdateAndPersistMetadataValidatesControlEdits(t *testing.T) {
	for _, field := range []string{credentialconcurrency.Field, AttributeWeight} {
		t.Run(field, func(t *testing.T) {
			manager, store, original := newMetadataControlsFixture(t, nil)
			before := store.saveCount()
			if _, errUpdate := manager.UpdateAndPersistMetadata(context.Background(), original.ID, func(latest *Auth) error {
				latest.Metadata[field] = "invalid-control"
				return nil
			}); errUpdate == nil {
				t.Fatal("invalid control value was accepted")
			}
			current, _ := manager.GetByID(original.ID)
			if !reflect.DeepEqual(current, original) || store.saveCount() != before {
				t.Fatal("invalid edit changed runtime or durable metadata")
			}
		})
	}
	manager, _, original := newMetadataControlsFixture(t, nil)
	sentinel := errors.New("editor rejected update")
	if _, errUpdate := manager.UpdateAndPersistMetadata(context.Background(), original.ID, func(*Auth) error { return sentinel }); !errors.Is(errUpdate, sentinel) {
		t.Fatalf("edit error = %v, want original error", errUpdate)
	}
}
