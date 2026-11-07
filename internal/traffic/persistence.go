package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

const stateKey = "traffic:v1:state"
const keyPrefix = "traffic:v1:key:"

type storedKey struct {
	ID         string        `json:"id"`
	Group      string        `json:"group"`
	Provider   string        `json:"provider"`
	Name       string        `json:"name"`
	Mask       string        `json:"mask"`
	Deleted    bool          `json:"deleted"`
	Disabled   bool          `json:"disabled"`
	DeletedDay int64         `json:"deleted_day"`
	LastSeen   int64         `json:"last_seen"`
	Data       storedBuckets `json:"data"`
}
type storedState struct {
	Version      int           `json:"version"`
	Salt         []byte        `json:"salt"`
	StartedAt    time.Time     `json:"started_at"`
	PersistedAt  time.Time     `json:"persisted_at"`
	SampledAt    *time.Time    `json:"sampled_at,omitempty"`
	Baseline     NetworkSample `json:"baseline"`
	HasBaseline  bool          `json:"has_baseline"`
	Network      storedBuckets `json:"network"`
	Business     storedBuckets `json:"business"`
	Unattributed storedBuckets `json:"unattributed"`
	Keys         []string      `json:"keys"`
	GC           []string      `json:"gc,omitempty"`
}

func (c *Collector) restore() error {
	raw, found, errGet := c.store.KVGet(context.Background(), stateKey)
	if errGet != nil {
		return errGet
	}
	if !found {
		return nil
	}
	var state storedState
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil {
		return fmt.Errorf("traffic: invalid stored state: %w", errDecode)
	}
	if state.Version != 1 || len(state.Salt) != 32 || state.StartedAt.IsZero() {
		return fmt.Errorf("traffic: unsupported or incomplete stored state")
	}
	paths := make([]string, len(state.Keys))
	for i, id := range state.Keys {
		paths[i] = keyPrefix + id
	}
	values, present, errRead := c.store.KVMGet(context.Background(), paths...)
	if errRead != nil {
		return errRead
	}
	loaded := make([]storedKey, len(values))
	for i, value := range values {
		if !present[i] {
			return fmt.Errorf("traffic: stored key history is missing")
		}
		if errDecode := json.Unmarshal(value, &loaded[i]); errDecode != nil {
			return fmt.Errorf("traffic: invalid key history: %w", errDecode)
		}
		if loaded[i].ID != state.Keys[i] {
			return fmt.Errorf("traffic: stored key identity mismatch")
		}
	}
	// Apply only after the complete checkpoint has been validated.
	c.salt = state.Salt
	c.startedAt = state.StartedAt
	c.lastPersisted = &state.PersistedAt
	c.lastSampled = state.SampledAt
	c.baseline = state.Baseline
	c.hasBaseline = state.HasBaseline
	c.pendingDeletes = make(map[string]struct{}, len(state.GC))
	for _, id := range state.GC {
		c.pendingDeletes[id] = struct{}{}
	}
	c.network.restore(state.Network)
	c.business.restore(state.Business)
	c.unattributed.restore(state.Unattributed)
	for _, value := range loaded {
		record := &keyRecord{id: value.ID, group: value.Group, provider: value.Provider, name: value.Name, mask: value.Mask, deleted: value.Deleted, disabled: value.Disabled, deletedDay: value.DeletedDay}
		record.lastSeen.Store(value.LastSeen)
		record.data.restore(value.Data)
		c.keys[value.ID] = record
	}
	return nil
}

// Flush writes absolute snapshots in one local transaction. Unchanged key
// histories are omitted; a failed write keeps their versions unacknowledged.
func (c *Collector) Flush(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	now := c.now()
	cutoff := cutoffDay(now)
	c.business.prune(now)
	c.unattributed.prune(now)
	c.sampleMu.Lock()
	c.network.prune(now)
	state := storedState{Version: 1, StartedAt: c.startedAt, PersistedAt: now.UTC(), Baseline: c.baseline, HasBaseline: c.hasBaseline, Network: c.network.store(), Business: c.business.store(), Unattributed: c.unattributed.store()}
	c.mu.Lock()
	state.Salt = append([]byte(nil), c.salt...)
	state.SampledAt = copyTime(c.lastSampled)
	if c.pendingDeletes == nil {
		c.pendingDeletes = make(map[string]struct{})
	}
	for id, record := range c.keys {
		lastDay := int64(0)
		if stamp := record.lastSeen.Load(); stamp != 0 {
			lastDay = dayNumber(time.Unix(0, stamp))
		}
		if record.deleted && record.deletedDay < cutoff && lastDay < cutoff {
			delete(c.keys, id)
			c.pendingDeletes[keyPrefix+id] = struct{}{}
		}
	}
	removed := make([]string, 0, len(c.pendingDeletes))
	for id := range c.pendingDeletes {
		removed = append(removed, id)
	}
	sort.Strings(removed)
	state.GC = removed
	type pendingWrite struct {
		record  *keyRecord
		version uint64
		value   storedKey
	}
	writes := make([]pendingWrite, 0, len(c.keys))
	state.Keys = make([]string, 0, len(c.keys))
	for id, record := range c.keys {
		state.Keys = append(state.Keys, id)
		record.data.prune(now)
		version := record.data.version.Load()
		if version == record.data.persisted.Load() {
			continue
		}
		value := storedKey{ID: id, Group: record.group, Provider: record.provider, Name: record.name, Mask: record.mask, Deleted: record.deleted, Disabled: record.disabled, DeletedDay: record.deletedDay, LastSeen: record.lastSeen.Load()}
		writes = append(writes, pendingWrite{record, version, value})
	}
	c.mu.Unlock()
	c.sampleMu.Unlock()
	if c.store == nil {
		return nil
	}
	sort.Strings(state.Keys)
	pairs := make(map[string][]byte, len(writes)+1)
	for i := range writes {
		writes[i].value.Data = writes[i].record.data.store()
		raw, errEncode := json.Marshal(writes[i].value)
		if errEncode != nil {
			return errEncode
		}
		pairs[keyPrefix+writes[i].value.ID] = raw
	}
	raw, errEncode := json.Marshal(state)
	if errEncode != nil {
		return errEncode
	}
	pairs[stateKey] = raw
	if errWrite := c.store.KVMSet(ctx, pairs); errWrite != nil {
		c.mu.Lock()
		c.storageError = errWrite.Error()
		c.mu.Unlock()
		return errWrite
	}
	for _, write := range writes {
		write.record.data.persisted.Store(write.version)
	}
	c.mu.Lock()
	stamp := now.UTC()
	c.lastPersisted = &stamp
	c.storageError = ""
	c.mu.Unlock()
	if len(removed) > 0 {
		if _, errDelete := c.store.KVDel(ctx, removed...); errDelete != nil {
			c.mu.Lock()
			c.storageError = errDelete.Error()
			c.mu.Unlock()
			return errDelete
		}
		c.mu.Lock()
		for _, id := range removed {
			delete(c.pendingDeletes, id)
		}
		c.mu.Unlock()
	}
	return nil
}
