package traffic

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var beijing = time.FixedZone(Timezone, 8*60*60)
var protocols = [...]Protocol{HTTP, WebSocket, RTP, DataChannel, TCP, "other"}

type atomicBytes struct{ rx, tx atomic.Uint64 }
type atomicCounter struct{ values [6]atomicBytes }
type dayBucket struct {
	day     int64
	counter atomicCounter
}
type bucketSet struct {
	total     atomicCounter
	current   atomic.Pointer[dayBucket]
	mu        sync.Mutex
	days      map[int64]*dayBucket
	version   atomic.Uint64
	persisted atomic.Uint64
}

func dayNumber(t time.Time) int64 { return (t.Unix() + 8*60*60) / 86400 }
func dayTime(day int64) time.Time { return time.Unix(day*86400-8*60*60, 0).In(beijing) }
func cutoffDay(now time.Time) int64 {
	local := now.In(beijing)
	return dayNumber(time.Date(local.Year(), local.Month()-1, 1, 0, 0, 0, 0, beijing))
}
func protocolIndex(protocol Protocol) int {
	for i, candidate := range protocols {
		if protocol == candidate {
			return i
		}
	}
	return len(protocols) - 1
}
func (a *atomicCounter) add(index int, rx, tx uint64) {
	if rx != 0 {
		a.values[index].rx.Add(rx)
	}
	if tx != 0 {
		a.values[index].tx.Add(tx)
	}
}
func (s *bucketSet) bucket(day int64) *dayBucket {
	if current := s.current.Load(); current != nil && current.day == day {
		return current
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.days == nil {
		s.days = make(map[int64]*dayBucket)
	}
	bucket := s.days[day]
	if bucket == nil {
		bucket = &dayBucket{day: day}
		s.days[day] = bucket
	}
	if current := s.current.Load(); current == nil || current.day < day {
		s.current.Store(bucket)
	}
	return bucket
}
func (s *bucketSet) add(day int64, index int, rx, tx uint64) {
	if rx == 0 && tx == 0 {
		return
	}
	s.bucket(day).counter.add(index, rx, tx)
	s.total.add(index, rx, tx)
	s.version.Add(1)
}
func bytes(rx, tx uint64) Bytes { return Bytes{RxBytes: rx, TxBytes: tx, TotalBytes: rx + tx} }
func (a *atomicCounter) snapshot(withProtocols bool) Counter {
	out := Counter{}
	for i, protocol := range protocols {
		rx, tx := a.values[i].rx.Load(), a.values[i].tx.Load()
		out.RxBytes += rx
		out.TxBytes += tx
		if withProtocols && (rx != 0 || tx != 0) {
			if out.Protocols == nil {
				out.Protocols = make(map[Protocol]Bytes)
			}
			out.Protocols[protocol] = bytes(rx, tx)
		}
	}
	out.TotalBytes = out.RxBytes + out.TxBytes
	return out
}
func sumCounter(dst *Counter, src Counter) {
	dst.RxBytes += src.RxBytes
	dst.TxBytes += src.TxBytes
	dst.TotalBytes = dst.RxBytes + dst.TxBytes
	for protocol, value := range src.Protocols {
		if dst.Protocols == nil {
			dst.Protocols = make(map[Protocol]Bytes)
		}
		previous := dst.Protocols[protocol]
		dst.Protocols[protocol] = bytes(previous.RxBytes+value.RxBytes, previous.TxBytes+value.TxBytes)
	}
}
func (s *bucketSet) series(now time.Time, withProtocols bool) Series {
	out := Series{Total: s.total.snapshot(withProtocols), Days: []Period{}, Months: []Period{}}
	cutoff, today := cutoffDay(now), dayNumber(now)
	month := now.In(beijing).Format("2006-01")
	s.mu.Lock()
	buckets := make([]*dayBucket, 0, len(s.days))
	for day, bucket := range s.days {
		if day >= cutoff && day <= today {
			buckets = append(buckets, bucket)
		}
	}
	s.mu.Unlock()
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].day < buckets[j].day })
	months := make(map[string]*Counter)
	for _, bucket := range buckets {
		value := bucket.counter.snapshot(withProtocols)
		date := dayTime(bucket.day)
		out.Days = append(out.Days, Period{Period: date.Format("2006-01-02"), Counter: value})
		if bucket.day == today {
			out.Today = value
		}
		monthKey := date.Format("2006-01")
		if months[monthKey] == nil {
			months[monthKey] = &Counter{}
		}
		sumCounter(months[monthKey], value)
	}
	monthKeys := make([]string, 0, len(months))
	for key := range months {
		monthKeys = append(monthKeys, key)
	}
	sort.Strings(monthKeys)
	for _, key := range monthKeys {
		out.Months = append(out.Months, Period{Period: key, Counter: *months[key]})
		if key == month {
			out.Month = *months[key]
		}
	}
	return out
}
func (s *bucketSet) prune(now time.Time) {
	cutoff := cutoffDay(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	for day := range s.days {
		if day < cutoff {
			delete(s.days, day)
			s.version.Add(1)
		}
	}
}

// summary omits historical arrays for the frequently polled key directory.
func (s *bucketSet) summary(now time.Time) Series {
	out := Series{Total: s.total.snapshot(true), Days: []Period{}, Months: []Period{}}
	local := now.In(beijing)
	first := dayNumber(time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, beijing))
	today := dayNumber(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	for day, bucket := range s.days {
		if day < first || day > today {
			continue
		}
		value := bucket.counter.snapshot(true)
		sumCounter(&out.Month, value)
		if day == today {
			out.Today = value
		}
	}
	return out
}

type storedCounter [6][2]uint64
type storedBuckets struct {
	Total storedCounter           `json:"total"`
	Days  map[int64]storedCounter `json:"days"`
}

func (a *atomicCounter) store() storedCounter {
	var out storedCounter
	for i := range a.values {
		out[i] = [2]uint64{a.values[i].rx.Load(), a.values[i].tx.Load()}
	}
	return out
}
func (a *atomicCounter) restore(value storedCounter) {
	for i := range value {
		a.values[i].rx.Store(value[i][0])
		a.values[i].tx.Store(value[i][1])
	}
}
func (s *bucketSet) store() storedBuckets {
	out := storedBuckets{Total: s.total.store(), Days: make(map[int64]storedCounter)}
	s.mu.Lock()
	defer s.mu.Unlock()
	for day, bucket := range s.days {
		out.Days[day] = bucket.counter.store()
	}
	return out
}
func (s *bucketSet) restore(value storedBuckets) {
	s.total.restore(value.Total)
	for day, counter := range value.Days {
		s.bucket(day).counter.restore(counter)
	}
}
