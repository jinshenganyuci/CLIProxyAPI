// Package traffic counts business payload bytes independently of token usage.
package traffic

import (
	"context"
	"time"
)

type Protocol string

const (
	HTTP        Protocol = "http"
	WebSocket   Protocol = "websocket"
	RTP         Protocol = "rtp"
	DataChannel Protocol = "data_channel"
	TCP         Protocol = "tcp"
	Timezone             = "Asia/Shanghai"
)

// Identity is used only while binding a meter. Raw keys are never persisted.
type Identity struct {
	Provider, Key, Name, Mask string
	Disabled                  bool
}

// Options configures a collector. Empty Path selects memory-only operation.
// The clock and reader overrides are intended for deterministic tests.
type Options struct {
	Enabled           bool
	Path              string
	Interface         string
	Now               func() time.Time
	ReadNetwork       func(string) (NetworkSample, error)
	DisableBackground bool
}

type NetworkSample struct {
	Epoch            string
	RxBytes, TxBytes uint64
}

type Bytes struct {
	RxBytes    uint64 `json:"rx_bytes"`
	TxBytes    uint64 `json:"tx_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

type Counter struct {
	Bytes
	Protocols map[Protocol]Bytes `json:"protocols,omitempty"`
}

type Period struct {
	Period string `json:"period"`
	Counter
}

type Series struct {
	Total  Counter  `json:"total"`
	Today  Counter  `json:"today"`
	Month  Counter  `json:"month"`
	Days   []Period `json:"days"`
	Months []Period `json:"months"`
}

type SummaryResponse struct {
	Enabled               bool       `json:"enabled"`
	State                 string     `json:"state"`
	StorageError          string     `json:"storage_error,omitempty"`
	NetworkError          string     `json:"network_error,omitempty"`
	Interface             string     `json:"interface"`
	Timezone              string     `json:"timezone"`
	RetentionMonths       int        `json:"retention_months"`
	SampleIntervalSeconds int        `json:"sample_interval_seconds"`
	FlushIntervalSeconds  int        `json:"flush_interval_seconds"`
	StartedAt             time.Time  `json:"started_at"`
	KeyID                 string     `json:"key_id,omitempty"`
	Granularity           string     `json:"granularity"`
	UpdatedAt             time.Time  `json:"updated_at"`
	LastPersistedAt       *time.Time `json:"last_persisted_at,omitempty"`
	LastSampledAt         *time.Time `json:"last_sampled_at,omitempty"`
	Network               Series     `json:"network"`
	Business              Series     `json:"business"`
	Unattributed          Series     `json:"unattributed"`
}

type KeyQuery struct {
	Group          string
	Search         string
	IncludeDeleted bool
	Offset, Limit  int
}

type Query struct{ KeyID, Granularity string }

type KeyUsage struct {
	ID         string     `json:"id"`
	Group      string     `json:"group"`
	Provider   string     `json:"provider"`
	Name       string     `json:"name"`
	Mask       string     `json:"mask"`
	Deleted    bool       `json:"deleted"`
	Enabled    bool       `json:"enabled"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	Traffic    Series     `json:"traffic"`
}

type KeysResponse struct {
	Keys   []KeyUsage `json:"keys"`
	Total  int        `json:"total"`
	Offset int        `json:"offset"`
	Limit  int        `json:"limit"`
}

type meterContextKey struct{}

func WithMeter(ctx context.Context, meter *Meter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, meterContextKey{}, meter)
}

func FromContext(ctx context.Context) *Meter {
	if ctx == nil {
		return nil
	}
	meter, _ := ctx.Value(meterContextKey{}).(*Meter)
	return meter
}
