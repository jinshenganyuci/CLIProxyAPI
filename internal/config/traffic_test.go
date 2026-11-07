package config

import "testing"

func TestTrafficV8ConfigAndRuntimeClone(t *testing.T) {
	if !(TrafficConfig{}).IsEnabled() {
		t.Fatal("omitted traffic setting must default on")
	}
	raw := []byte("config-version: 8\nobservability:\n  traffic:\n    enabled: false\n    interface: eth1\n")
	if err := ValidateV8Config(raw); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Traffic.IsEnabled() || cfg.Traffic.Interface != "eth1" {
		t.Fatalf("traffic config lost: %+v", cfg.Traffic)
	}
	cloned := cfg.CloneForRuntime()
	*cloned.Traffic.Enabled = true
	if cfg.Traffic.IsEnabled() || !cloned.Traffic.IsEnabled() {
		t.Fatal("runtime snapshot shares traffic enabled pointer")
	}
}
