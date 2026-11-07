package config

// TrafficConfig controls byte accounting independently from token usage queues.
// Omitted Enabled defaults to true. Storage remains local at <auth-dir>/traffic.db.
type TrafficConfig struct {
	Enabled   *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Interface string `yaml:"interface,omitempty" json:"interface,omitempty"`
}

func (c TrafficConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }
