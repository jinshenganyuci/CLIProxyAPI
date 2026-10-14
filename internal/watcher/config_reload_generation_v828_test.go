package watcher

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestV828ConfigWrittenDuringReloadIsNotMarkedApplied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write := func(proxy string) {
		t.Helper()
		raw := "auth-dir: " + dir + "\nproxy-url: " + proxy + "\n"
		if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	write("http://first.invalid:8080")
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	t.Cleanup(release)
	var applied []string
	w := &Watcher{configPath: path, authDir: dir, reloadCallback: func(cfg *config.Config) {
		applied = append(applied, cfg.ProxyURL)
		if len(applied) == 1 {
			close(entered)
			<-resume
		}
	}}
	w.SetConfig(&config.Config{AuthDir: dir})
	go func() {
		defer close(done)
		w.ReloadConfigIfChanged()
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first runtime callback did not start")
	}
	write("http://second.invalid:8080")
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first runtime callback did not finish")
	}
	w.ReloadConfigIfChanged()
	if len(applied) != 2 || applied[0] != "http://first.invalid:8080" || applied[1] != "http://second.invalid:8080" {
		t.Fatalf("configuration changed during reload was skipped: %v", applied)
	}
	w.ReloadConfigIfChanged()
	if len(applied) != 2 {
		t.Fatal("unchanged configuration caused another runtime reload")
	}
}

func TestV828ConfigReloadAcknowledgesConsumedSourceAcrossABA(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	first := []byte("auth-dir: " + dir + "\nproxy-url: http://first.invalid:8080\n")
	second := []byte("auth-dir: " + dir + "\nproxy-url: http://second.invalid:8080\n")
	if errWrite := os.WriteFile(path, second, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	var applied []string
	w := &Watcher{configPath: path, authDir: dir, reloadCallback: func(cfg *config.Config) {
		applied = append(applied, cfg.ProxyURL)
		if len(applied) == 1 {
			if errWrite := os.WriteFile(path, first, 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
		}
	}}
	w.SetConfig(&config.Config{AuthDir: dir})
	// The hash precheck observed A; the loader consumes B; the callback saves A.
	// Entering after the precheck makes this interleaving deterministic.
	w.configApplyMu.Lock()
	w.reloadConfigIfChangedLocked(first)
	w.configApplyMu.Unlock()
	w.ReloadConfigIfChanged()
	if len(applied) != 2 || applied[0] != "http://second.invalid:8080" || applied[1] != "http://first.invalid:8080" {
		t.Fatalf("ABA write was incorrectly considered already applied: %v", applied)
	}
}

func TestV828ConcurrentConfigReloadsSerializeCallbacks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write := func(proxy string) {
		t.Helper()
		if errWrite := os.WriteFile(path, []byte("auth-dir: "+dir+"\nproxy-url: "+proxy+"\n"), 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	write("http://first.invalid:8080")
	entered, resume := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(resume) }) }
	t.Cleanup(release)
	var active atomic.Int32
	var calls atomic.Int32
	var last atomic.Value
	var w *Watcher
	w = &Watcher{configPath: path, authDir: dir, reloadCallback: func(cfg *config.Config) {
		if active.Add(1) != 1 {
			t.Error("configuration callbacks overlapped")
		}
		defer active.Add(-1)
		if w.configApplyMu.TryLock() {
			w.configApplyMu.Unlock()
			t.Error("config application was unlocked before its callback completed")
		}
		if calls.Add(1) == 1 {
			close(entered)
			<-resume
		}
		// SetConfig is used during service startup/reload and must not reenter
		// configApplyMu or retain a caller-owned mutable config.
		w.SetConfig(cfg)
		last.Store(cfg.ProxyURL)
	}}
	w.SetConfig(&config.Config{AuthDir: dir})
	var done sync.WaitGroup
	done.Add(2)
	go func() {
		defer done.Done()
		w.ReloadConfigIfChanged()
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first callback did not start")
	}
	write("http://second.invalid:8080")
	secondStarted := make(chan struct{})
	go func() {
		defer done.Done()
		close(secondStarted)
		w.ReloadConfigIfChanged()
	}()
	<-secondStarted
	release()
	done.Wait()
	if calls.Load() != 2 || last.Load() != "http://second.invalid:8080" {
		t.Fatal("concurrent reload did not finish with the latest configuration")
	}
	w.ReloadConfigIfChanged()
	if calls.Load() != 2 {
		t.Fatal("completed concurrent reload did not record its consumed source")
	}
}
