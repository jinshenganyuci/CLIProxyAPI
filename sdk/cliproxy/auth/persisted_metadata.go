package auth

import (
	"context"
	"fmt"
	"time"
)

// UpdatePersistedMetadata commits a metadata-only change against the latest
// runtime auth while excluding concurrent refresh persistence. The callback is
// responsible for persisting its metadata change and must not call the manager
// or external hooks. It may acquire a credential file lock, in that order.
func (m *Manager) UpdatePersistedMetadata(ctx context.Context, id string, commit func(*Auth) error) (*Auth, error) {
	if m == nil || commit == nil {
		return nil, fmt.Errorf("persisted metadata update is unavailable")
	}
	m.mu.Lock()
	current := m.auths[id]
	if current == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("auth %q disappeared before metadata update", id)
	}
	// Result recording already persists under m.mu. Preserve the same lock order
	// so pending refresh writes can finish without waiting for the manager lock.
	lockValue, _ := m.persistLocks.LoadOrStore(id, &authPersistLock{})
	persistLock := lockValue.(*authPersistLock)
	persistLock.mu.Lock()
	updated := current.Clone()
	// A refresh can publish its runtime result just before this transaction
	// acquires m.mu, without having started persistence yet. Flush that result
	// before advancing the persistence watermark past its generation.
	if m.store != nil && (persistLock.lastSaveFailed || current.RegistrationEpoch > persistLock.lastEpoch ||
		(current.RegistrationEpoch == persistLock.lastEpoch && current.Generation > persistLock.lastGeneration)) {
		if _, errSave := m.store.Save(ctx, current.Clone()); errSave != nil {
			persistLock.lastSaveFailed = true
			persistLock.mu.Unlock()
			m.mu.Unlock()
			return nil, fmt.Errorf("persist pending auth metadata: %w", errSave)
		}
		persistLock.lastEpoch = current.RegistrationEpoch
		persistLock.lastGeneration = current.Generation
		persistLock.lastSaveFailed = false
	}
	if errCommit := commit(updated); errCommit != nil {
		persistLock.mu.Unlock()
		m.mu.Unlock()
		return nil, errCommit
	}
	updated.Generation = current.Generation + 1
	updated.UpdatedAt = time.Now()
	m.auths[id] = updated.Clone()
	persistLock.lastEpoch = updated.RegistrationEpoch
	persistLock.lastGeneration = updated.Generation
	persistLock.mu.Unlock()
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(updated.Clone())
	}
	m.queueRefreshReschedule(id)
	m.hook.OnAuthUpdated(ctx, updated.Clone())
	return updated.Clone(), nil
}
