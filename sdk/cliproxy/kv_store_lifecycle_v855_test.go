package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

func TestServiceKVStoreUnmanagedShutdownPreservesActiveStore(t *testing.T) {
	home.ClearCurrent()
	t.Cleanup(func() { _ = kvstore.ShutdownLocal() })
	svc := &Service{}
	cfg := &config.Config{AuthDir: t.TempDir()}
	svc.applyKVStoreConfig(cfg)
	client, active, errClient := kvstore.CurrentKVClient()
	if errClient != nil || !active || client == nil {
		t.Fatalf("active store = %v, %v, %v", client, active, errClient)
	}

	if errShutdown := (&Service{}).Shutdown(context.Background()); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	var nilSvc *Service
	nilSvc.applyKVStoreConfig(nil)
	if current, currentActive, errCurrent := kvstore.CurrentKVClient(); errCurrent != nil || !currentActive || current != client {
		t.Fatalf("unmanaged shutdown changed active store: %v, %v, %v", current, currentActive, errCurrent)
	}
	if _, _, errRead := client.KVGet(context.Background(), "still-open"); errRead != nil {
		t.Fatalf("active store was closed: %v", errRead)
	}
}

func TestServiceKVStoreShutdownFencesDelayedConfigApplication(t *testing.T) {
	home.ClearCurrent()
	t.Cleanup(func() { _ = kvstore.ShutdownLocal() })
	svc := &Service{}
	cfg := &config.Config{AuthDir: t.TempDir()}
	svc.applyKVStoreConfig(cfg)
	commit := svc.commitConfigUpdate(cfg)

	// Hold runtime work after the snapshot commits, then close before it resumes.
	svc.configRuntimeMu.Lock()
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(ready)
		svc.applyConfigRuntime(context.Background(), commit, false)
		close(done)
	}()
	<-ready
	errShutdown := svc.shutdownKVStore()
	svc.configRuntimeMu.Unlock()
	<-done
	if errShutdown != nil {
		t.Fatal(errShutdown)
	}
	// A delayed startup callback must not reopen the stopped service either.
	svc.applyKVStoreConfig(cfg)
	if client, active, errClient := kvstore.CurrentKVClient(); client != nil || active || errClient != nil {
		t.Fatalf("closed service reopened KV: %v, %v, %v", client, active, errClient)
	}
}

func TestServiceKVStoreConcurrentApplyAndShutdown(t *testing.T) {
	home.ClearCurrent()
	t.Cleanup(func() { _ = kvstore.ShutdownLocal() })
	cfg := &config.Config{AuthDir: t.TempDir()}
	for range 32 {
		svc := &Service{}
		svc.applyKVStoreConfig(cfg)
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			svc.applyKVStoreConfig(cfg)
			svc.reloadKVStoreConfig(cfg)
		}()
		go func() {
			defer workers.Done()
			<-start
			if errShutdown := svc.shutdownKVStore(); errShutdown != nil {
				t.Errorf("shutdown: %v", errShutdown)
			}
		}()
		close(start)
		workers.Wait()
		if client, active, errClient := kvstore.CurrentKVClient(); client != nil || active || errClient != nil {
			t.Fatalf("concurrent close left KV active: %v, %v, %v", client, active, errClient)
		}
	}
}

func TestServiceKVStoreFailedReloadRemainsFailClosed(t *testing.T) {
	home.ClearCurrent()
	t.Cleanup(func() { _ = kvstore.ShutdownLocal() })
	dir := t.TempDir()
	svc := &Service{}
	cfg := &config.Config{AuthDir: dir}
	svc.applyKVStoreConfig(cfg)
	previous, active, errClient := kvstore.CurrentKVClient()
	if errClient != nil || !active || previous == nil {
		t.Fatalf("initial store: %v, %v, %v", previous, active, errClient)
	}
	blocker := filepath.Join(dir, "not-a-directory")
	if errWrite := os.WriteFile(blocker, []byte("fixture"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	svc.applyConfigUpdate(&config.Config{AuthDir: dir, KVStore: config.KVStoreConfig{Path: filepath.Join(blocker, "kv.db")}})
	if current, currentActive, errCurrent := kvstore.CurrentKVClient(); current != nil || !currentActive || errCurrent == nil {
		t.Fatalf("failed reload did not fail closed: %v, %v, %v", current, currentActive, errCurrent)
	}
	if _, _, errRead := previous.KVGet(context.Background(), "closed"); errRead == nil {
		t.Fatal("previous store remained available after failed reload")
	}
	svc.applyConfigUpdate(cfg)
	if current, currentActive, errCurrent := kvstore.CurrentKVClient(); current == nil || !currentActive || errCurrent != nil {
		t.Fatalf("valid reload did not recover: %v, %v, %v", current, currentActive, errCurrent)
	}
}
