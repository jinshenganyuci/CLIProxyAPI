package traffic

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

type keyRecord struct {
	id, group, provider, name, mask string
	deleted                         bool
	disabled                        bool
	deletedDay                      int64
	lastSeen                        atomic.Int64
	data                            bucketSet
}

type checkpointStore interface {
	KVGet(context.Context, string) ([]byte, bool, error)
	KVMGet(context.Context, ...string) ([][]byte, []bool, error)
	KVMSet(context.Context, map[string][]byte) error
	KVDel(context.Context, ...string) (int64, error)
	Close() error
}

// Collector owns its local database and never routes writes through Home KV.
type Collector struct {
	enabled                         atomic.Bool
	closed                          atomic.Bool
	active                          atomic.Int64
	pending                         sync.Map
	now                             func() time.Time
	readNetwork                     func(string) (NetworkSample, error)
	mu                              sync.RWMutex
	keys                            map[string]*keyRecord
	pendingDeletes                  map[string]struct{}
	salt                            []byte
	startedAt                       time.Time
	storageError                    string
	networkError                    string
	lastPersisted                   *time.Time
	lastSampled                     *time.Time
	iface                           string
	network, business, unattributed bucketSet
	sampleMu                        sync.Mutex
	baseline                        NetworkSample
	hasBaseline                     bool
	store                           checkpointStore
	flushMu                         sync.Mutex
	stop                            chan struct{}
	done                            chan struct{}
	closeOnce                       sync.Once
	closeErr                        error
}

// Open returns a usable memory collector even if persistence cannot be opened.
// A corrupt database is never overwritten by a fresh in-memory history.
func Open(options Options) (*Collector, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	reader := options.ReadNetwork
	if reader == nil {
		reader = readNetworkSample
	}
	iface := strings.TrimSpace(options.Interface)
	if iface == "" {
		iface = "eth0"
	}
	c := &Collector{now: now, readNetwork: reader, keys: make(map[string]*keyRecord), salt: make([]byte, 32), startedAt: now().UTC(), iface: iface, stop: make(chan struct{}), done: make(chan struct{})}
	c.enabled.Store(options.Enabled)
	if _, errRandom := rand.Read(c.salt); errRandom != nil {
		return nil, fmt.Errorf("traffic identity salt: %w", errRandom)
	}
	var openErr error
	if options.Path != "" {
		c.store, openErr = kvstore.OpenBoltStore(options.Path, kvstore.WithSweepInterval(0))
		if openErr == nil {
			openErr = c.restore()
		}
		if openErr != nil {
			if c.store != nil {
				_ = c.store.Close()
				c.store = nil
			}
			c.storageError = openErr.Error()
		}
	}
	if !options.Enabled {
		c.hasBaseline = false
	}
	if options.Enabled {
		_ = c.SampleNetwork()
	}
	if options.DisableBackground {
		close(c.done)
	} else {
		go c.run()
	}
	return c, openErr
}

func (c *Collector) run() {
	defer close(c.done)
	samples := time.NewTicker(5 * time.Second)
	defer samples.Stop()
	flushes := time.NewTicker(15 * time.Second)
	defer flushes.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-samples.C:
			_ = c.SampleNetwork()
		case <-flushes.C:
			_ = c.Flush(context.Background())
		}
	}
}

// Configure changes collection settings without reopening or replacing storage.
func (c *Collector) Configure(enabled bool, iface string) {
	if c == nil || c.closed.Load() {
		return
	}
	iface = strings.TrimSpace(iface)
	if iface == "" {
		iface = "eth0"
	}
	c.sampleMu.Lock()
	defer c.sampleMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enabled.Load() != enabled || c.iface != iface {
		c.hasBaseline = false
		c.networkError = ""
	}
	c.iface = iface
	c.enabled.Store(enabled)
}

func canonicalProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if strings.EqualFold(provider, "config-inline") || strings.EqualFold(provider, "config-api-key") {
		return "config-inline"
	}
	if provider == "" {
		return "unknown"
	}
	return provider
}
func (c *Collector) identityID(provider, key string) string {
	digest := hmac.New(sha256.New, c.salt)
	_, _ = digest.Write([]byte(provider))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(key))
	return hex.EncodeToString(digest.Sum(nil))
}
func displayIdentity(identity Identity) (string, string) {
	key := strings.TrimSpace(identity.Key)
	mask := "****"
	if len(key) > 4 {
		mask += key[len(key)-4:]
	}
	if identity.Mask != "" && !strings.Contains(identity.Mask, key) {
		mask = identity.Mask
	}
	name := strings.ReplaceAll(strings.TrimSpace(identity.Name), key, mask)
	if name == "" {
		name = mask
	}
	return name, mask
}
func (c *Collector) bindIdentity(identity Identity) *keyRecord {
	provider, key := canonicalProvider(identity.Provider), strings.TrimSpace(identity.Key)
	if key == "" {
		return nil
	}
	id := c.identityID(provider, key)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.keys[id]; existing != nil {
		return existing
	}
	name, mask := displayIdentity(identity)
	group := "plugin"
	if provider == "config-inline" {
		group = "native"
	}
	record := &keyRecord{id: id, group: group, provider: provider, name: name, mask: mask, disabled: identity.Disabled}
	record.data.version.Add(1)
	c.keys[id] = record
	delete(c.pendingDeletes, keyPrefix+id)
	return record
}

func (c *Collector) addBusiness(key *keyRecord, day int64, index int, rx, tx uint64) {
	c.business.add(day, index, rx, tx)
	if key == nil {
		c.unattributed.add(day, index, rx, tx)
		return
	}
	key.data.add(day, index, rx, tx)
	key.lastSeen.Store(c.now().UnixNano())
}

// SyncNativeKeys updates only the native key directory, independent of plugins.
func (c *Collector) SyncNativeKeys(identities []Identity) { c.syncKeys("config-inline", identities) }

// SyncPluginKeys updates one provider's directory; other plugins are untouched.
func (c *Collector) SyncPluginKeys(provider string, identities []Identity) {
	provider = canonicalProvider(provider)
	if provider != "config-inline" {
		c.syncKeys(provider, identities)
	}
}
func (c *Collector) syncKeys(provider string, identities []Identity) {
	if c == nil || c.closed.Load() {
		return
	}
	present := make(map[string]Identity, len(identities))
	for _, identity := range identities {
		identity.Key = strings.TrimSpace(identity.Key)
		if identity.Key != "" {
			identity.Provider = provider
			present[c.identityID(provider, identity.Key)] = identity
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, record := range c.keys {
		if record.provider != provider {
			continue
		}
		if _, exists := present[id]; !exists && !record.deleted {
			record.deleted = true
			record.deletedDay = dayNumber(c.now())
			record.data.version.Add(1)
		}
	}
	for id, identity := range present {
		record := c.keys[id]
		if record == nil {
			group := "plugin"
			if provider == "config-inline" {
				group = "native"
			}
			record = &keyRecord{id: id, group: group, provider: provider}
			c.keys[id] = record
			delete(c.pendingDeletes, keyPrefix+id)
		}
		name, mask := displayIdentity(identity)
		if record.name != name || record.mask != mask || record.disabled != identity.Disabled || record.deleted {
			record.name, record.mask = name, mask
			record.disabled = identity.Disabled
			record.deleted = false
			record.deletedDay = 0
			record.data.version.Add(1)
		}
	}
}

func (c *Collector) HasKey(id string) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.keys[id]
	return ok
}

func (c *Collector) Summary(queries ...Query) SummaryResponse {
	if c == nil {
		return SummaryResponse{State: "disabled", Timezone: Timezone}
	}
	query := Query{Granularity: "day"}
	if len(queries) > 0 {
		query = queries[0]
	}
	if query.Granularity != "month" {
		query.Granularity = "day"
	}
	now := c.now()
	c.mu.RLock()
	response := SummaryResponse{Enabled: c.enabled.Load(), State: "ready", StorageError: c.storageError, NetworkError: c.networkError, Interface: c.iface, Timezone: Timezone, RetentionMonths: 2, SampleIntervalSeconds: 5, FlushIntervalSeconds: 15, StartedAt: c.startedAt, UpdatedAt: now.UTC(), LastPersistedAt: copyTime(c.lastPersisted), LastSampledAt: copyTime(c.lastSampled), KeyID: query.KeyID, Granularity: query.Granularity}
	key := c.keys[query.KeyID]
	c.mu.RUnlock()
	if c.store == nil {
		response.State = "memory_only"
	}
	if !response.Enabled {
		response.State = "disabled"
	}
	response.Network = c.network.series(now, false)
	response.Unattributed = c.unattributed.series(now, true)
	if query.KeyID != "" {
		if key == nil {
			response.State = "key_not_found"
		} else {
			response.Business = key.data.series(now, true)
		}
	} else {
		response.Business = c.business.series(now, true)
	}
	return response
}
func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func (c *Collector) Keys(query KeyQuery) KeysResponse {
	if query.Limit <= 0 {
		query.Limit = 50
	}
	if query.Limit > 500 {
		query.Limit = 500
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	response := KeysResponse{Keys: []KeyUsage{}, Offset: query.Offset, Limit: query.Limit}
	if c == nil {
		return response
	}
	now := c.now()
	search := strings.ToLower(strings.TrimSpace(query.Search))
	c.mu.RLock()
	type selected struct {
		value  KeyUsage
		record *keyRecord
	}
	rows := make([]selected, 0, len(c.keys))
	for _, record := range c.keys {
		if query.Group != "" && record.group != query.Group || record.deleted && !query.IncludeDeleted {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(record.name+" "+record.mask+" "+record.provider+" "+record.id), search) {
			continue
		}
		value := KeyUsage{ID: record.id, Group: record.group, Provider: record.provider, Name: record.name, Mask: record.mask, Deleted: record.deleted, Enabled: !record.disabled}
		if last := record.lastSeen.Load(); last != 0 {
			stamp := time.Unix(0, last).UTC()
			value.LastSeenAt = &stamp
		}
		rows = append(rows, selected{value, record})
	}
	c.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].value.ID < rows[j].value.ID })
	response.Total = len(rows)
	if query.Offset >= len(rows) {
		return response
	}
	end := query.Offset + query.Limit
	if end > len(rows) {
		end = len(rows)
	}
	for _, row := range rows[query.Offset:end] {
		row.value.Traffic = row.record.data.summary(now)
		response.Keys = append(response.Keys, row.value)
	}
	return response
}

func (c *Collector) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.closeOnce.Do(func() {
		defer func() {
			if c.store != nil {
				if errClose := c.store.Close(); c.closeErr == nil {
					c.closeErr = errClose
				}
			}
		}()
		c.closed.Store(true)
		close(c.stop)
		<-c.done
		for c.active.Load() != 0 {
			select {
			case <-ctx.Done():
				c.closeErr = ctx.Err()
				return
			case <-time.After(time.Millisecond):
			}
		}
		c.pending.Range(func(key, value any) bool { key.(*Meter).Finish(); return true })
		_ = c.SampleNetwork()
		c.closeErr = c.Flush(ctx)
	})
	return c.closeErr
}
