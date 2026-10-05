package auth

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

// UpdateAndPersistMetadata applies operator edits to the latest credential and
// publishes them only after the configured store accepts the complete metadata.
// The edit callback runs under the metadata transaction lock and must not call
// manager methods or external hooks.
func (m *Manager) UpdateAndPersistMetadata(ctx context.Context, id string, edit func(*Auth) error) (*Auth, error) {
	if m == nil || edit == nil {
		return nil, fmt.Errorf("persisted metadata update is unavailable")
	}
	prepare := func(latest *Auth) error {
		m.mu.RLock()
		store := m.store
		m.mu.RUnlock()
		if store == nil {
			return fmt.Errorf("auth metadata persistence store is unavailable")
		}
		if IsConfigAPIKeyAuth(latest) {
			return fmt.Errorf("config API key metadata must be updated in its source configuration")
		}
		if strings.EqualFold(strings.TrimSpace(latest.Attributes[AttributeRuntimeOnly]), "true") {
			return fmt.Errorf("runtime-only auth metadata cannot be persisted")
		}
		if IsPluginVirtualAuth(latest) {
			return fmt.Errorf("plugin virtual auth metadata must be updated in its source auth file")
		}
		if latest.Storage != nil {
			cloner, ok := latest.Storage.(baseauth.MetadataStorageCloner)
			if !ok {
				return fmt.Errorf("auth token storage does not support isolated metadata updates")
			}
			cloned, errClone := cloner.CloneForMetadataUpdate()
			if errClone != nil {
				return fmt.Errorf("clone auth token storage: %w", errClone)
			}
			if cloned == nil {
				return fmt.Errorf("auth token storage clone is unavailable")
			}
			latest.Storage = cloned
		}
		metadata, errClone := baseauth.CloneMetadataForUpdate(latest.Metadata)
		if errClone != nil {
			return errClone
		}
		latest.Metadata = metadata
		latest.Attributes = maps.Clone(latest.Attributes)
		return nil
	}
	return m.updatePersistedMetadata(ctx, id, prepare, func(latest *Auth) error {
		if errEdit := edit(latest); errEdit != nil {
			return errEdit
		}
		NormalizeCredentialMetadata(latest.Metadata)
		if errWeight := ValidateAuthWeight(latest); errWeight != nil {
			return fmt.Errorf("update auth metadata: %w", errWeight)
		}
		if errConcurrency := ValidateAuthConcurrencyLimit(latest); errConcurrency != nil {
			return fmt.Errorf("update auth metadata: %w", errConcurrency)
		}
		if errSave := m.saveMetadataUpdate(ctx, latest); errSave != nil {
			return fmt.Errorf("persist auth metadata: %w", errSave)
		}
		return nil
	})
}

// saveMetadataUpdate wraps only the durable candidate. The published credential
// retains native storage for subsequent refreshes and provider identity access.
func (m *Manager) saveMetadataUpdate(ctx context.Context, auth *Auth) error {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == nil {
		return fmt.Errorf("auth metadata persistence store is unavailable")
	}
	snapshot := auth.Clone()
	nativeStorage := auth.Storage
	snapshot.Storage = baseauth.WrapAtomicMetadataStorage(nativeStorage)
	if _, errSave := store.Save(ctx, snapshot); errSave != nil {
		return errSave
	}
	*auth = *snapshot
	auth.Storage = nativeStorage
	return nil
}

// UpdatePersistedMetadata commits a metadata-only change against the latest
// runtime auth while excluding concurrent refresh persistence. The callback is
// responsible for persisting its metadata change and must not call the manager
// or external hooks. It may acquire a credential file lock, in that order.
func (m *Manager) UpdatePersistedMetadata(ctx context.Context, id string, commit func(*Auth) error) (*Auth, error) {
	return m.updatePersistedMetadata(ctx, id, nil, commit)
}

func (m *Manager) updatePersistedMetadata(ctx context.Context, id string, prepare func(*Auth) error, commit func(*Auth) error) (*Auth, error) {
	if m == nil || commit == nil {
		return nil, fmt.Errorf("persisted metadata update is unavailable")
	}
	releaseMutation, errMutation := m.lockAuthMutationContext(ctx, id)
	if errMutation != nil {
		return nil, errMutation
	}
	defer releaseMutation()
	m.mu.RLock()
	current := m.auths[id].Clone()
	store := m.store
	m.mu.RUnlock()
	if current == nil {
		return nil, fmt.Errorf("auth %q disappeared before metadata update", id)
	}
	// Use the native mutation/load gates, then the persistence watermark lock.
	// Store I/O must not hold m.mu: unrelated credentials and store read callbacks
	// remain available while the durable operator transaction is in progress.
	lockValue, _ := m.persistLocks.LoadOrStore(id, &authPersistLock{})
	persistLock := lockValue.(*authPersistLock)
	persistLock.mu.Lock()
	persistLocked := true
	defer func() {
		if persistLocked {
			persistLock.mu.Unlock()
		}
	}()
	updated := current.Clone()
	if prepare != nil {
		if errPrepare := prepare(updated); errPrepare != nil {
			return nil, errPrepare
		}
	}
	// Retry a failed or previously skipped token save before advancing the
	// durable metadata watermark past that credential generation.
	if store != nil && (persistLock.lastSaveFailed || current.RegistrationEpoch > persistLock.lastEpoch ||
		(current.RegistrationEpoch == persistLock.lastEpoch && current.Generation > persistLock.lastGeneration)) {
		pending := current.Clone()
		if prepare != nil {
			if errPrepare := prepare(pending); errPrepare != nil {
				return nil, errPrepare
			}
		}
		var errSave error
		if prepare != nil {
			errSave = m.saveMetadataUpdate(ctx, pending)
		} else {
			_, errSave = store.Save(ctx, pending)
		}
		if errSave != nil {
			persistLock.lastSaveFailed = true
			return nil, fmt.Errorf("persist pending auth metadata: %w", errSave)
		}
		mergeAuthSaveDelta(updated, current, pending, true)
		persistLock.lastEpoch = current.RegistrationEpoch
		persistLock.lastGeneration = current.Generation
		persistLock.lastSaveFailed = false
	}
	if errCommit := commit(updated); errCommit != nil {
		persistLock.lastSaveFailed = true
		return nil, errCommit
	}
	m.mu.Lock()
	latest := m.auths[id]
	if latest == nil || latest.RegistrationEpoch != current.RegistrationEpoch {
		m.mu.Unlock()
		return nil, fmt.Errorf("auth %q changed registration during metadata update", id)
	}
	// Runtime-only refresh outcomes can change during store I/O. Preserve their
	// delta without discarding accepted operator edits or store-enriched fields.
	mergeAuthSaveDelta(updated, current, latest, false)
	updated.ID = current.ID
	updated.RegistrationEpoch = current.RegistrationEpoch
	updated.Generation = latest.Generation + 1
	updated.UpdatedAt = time.Now()
	m.auths[id] = updated.Clone()
	m.notifyAuthChangeLocked(id)
	persistLock.lastEpoch = updated.RegistrationEpoch
	persistLock.lastGeneration = updated.Generation
	persistLock.lastSaveFailed = false
	m.mu.Unlock()
	// Release transaction locks before scheduler/hook callbacks can reenter.
	persistLock.mu.Unlock()
	persistLocked = false
	releaseMutation()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(updated.Clone())
	}
	m.queueRefreshReschedule(id)
	m.hook.OnAuthUpdated(ctx, updated.Clone())
	return updated.Clone(), nil
}
