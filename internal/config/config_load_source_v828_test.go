package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestV828LoadConfigSourceIdentifiesParsedBytesBeforeNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("proxy-url: http://old.invalid:8080\nrequests: {proxy-url: 'socks5h://selected.invalid:1080'}\n")
	if errWrite := os.WriteFile(path, raw, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, source, errLoad := LoadConfigWithSource(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if !bytes.Equal(source, raw) || cfg.ProxyURL != "socks5h://selected.invalid:1080" {
		t.Fatal("loader did not identify the exact source of the effective config")
	}
	normalized, errRead := os.ReadFile(path)
	if errRead != nil || bytes.Equal(normalized, raw) {
		t.Fatal("fixture did not exercise load-time normalization")
	}
	if bytes.Contains(normalized, []byte("old.invalid")) {
		t.Fatal("snapshot-aware loading did not retain legacy-conflict normalization")
	}
	next, nextSource, errLoad := LoadConfigWithSource(path)
	if errLoad != nil || !bytes.Equal(nextSource, normalized) || next.ProxyURL != cfg.ProxyURL {
		t.Fatal("normalized source does not load with the same effective config")
	}
}
