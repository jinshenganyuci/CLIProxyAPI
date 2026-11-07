package traffic

import (
	"sync"
	"sync/atomic"
)

type meterTarget struct{ key *keyRecord }

// Meter belongs to an authenticated request/session, not to a reused TCP connection.
type Meter struct {
	collector *Collector
	target    atomic.Pointer[meterTarget]
	mu        sync.Mutex
	pending   map[int64]storedCounter
}

func (c *Collector) NewMeter() *Meter {
	if c == nil || !c.enabled.Load() || c.closed.Load() {
		return nil
	}
	return &Meter{collector: c}
}

// Bind freezes the first nonempty identity and transfers pending bytes exactly once.
func (m *Meter) Bind(identity Identity) {
	if m == nil || m.collector == nil || identity.Key == "" || m.target.Load() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.target.Load() != nil {
		return
	}
	key := m.collector.bindIdentity(identity)
	m.bindLocked(&meterTarget{key: key})
}

func (m *Meter) bindLocked(target *meterTarget) {
	hadPending := m.pending != nil
	for day, counts := range m.pending {
		for index, value := range counts {
			m.collector.addBusiness(target.key, day, index, value[0], value[1])
		}
	}
	m.pending = nil
	m.target.Store(target)
	if hadPending {
		m.collector.pending.Delete(m)
	}
}

func (m *Meter) AddRx(protocol Protocol, n int) { m.add(protocol, n, true) }
func (m *Meter) AddTx(protocol Protocol, n int) { m.add(protocol, n, false) }
func (m *Meter) add(protocol Protocol, n int, rx bool) {
	if m == nil || n <= 0 || m.collector == nil || !m.collector.enabled.Load() || m.collector.closed.Load() {
		return
	}
	m.collector.active.Add(1)
	defer m.collector.active.Add(-1)
	if m.collector.closed.Load() {
		return
	}
	day := dayNumber(m.collector.now())
	index := protocolIndex(protocol)
	var received, sent uint64
	if rx {
		received = uint64(n)
	} else {
		sent = uint64(n)
	}
	if target := m.target.Load(); target != nil {
		m.collector.addBusiness(target.key, day, index, received, sent)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if target := m.target.Load(); target != nil {
		m.collector.addBusiness(target.key, day, index, received, sent)
		return
	}
	if m.pending == nil {
		m.pending = make(map[int64]storedCounter)
		m.collector.pending.Store(m, struct{}{})
	}
	counts := m.pending[day]
	counts[index][0] += received
	counts[index][1] += sent
	m.pending[day] = counts
}

// Finish attributes unauthenticated bytes once. A bound session remains usable
// after its HTTP bootstrap handler returns, including later media callbacks.
func (m *Meter) Finish() {
	if m == nil || m.collector == nil || m.target.Load() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.target.Load() == nil {
		m.bindLocked(&meterTarget{})
	}
}
