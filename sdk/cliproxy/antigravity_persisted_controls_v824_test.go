package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestV824AntigravityHintSaveProxyEditAndRestartKeepCredentialControls(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	var oldCalls, newCalls, globalCalls atomic.Int32
	oldProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oldCalls.Add(1)
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
	}))
	defer oldProxy.Close()
	newProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newCalls.Add(1)
		if r.URL.Path != antigravityModelsPath || r.Header.Get("Authorization") != "Bearer accepted-token" {
			t.Error("new probe lost selected credential")
		}
		_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
	}))
	defer newProxy.Close()
	globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalCalls.Add(1)
		http.Error(w, "unexpected global fallback", 502)
	}))
	defer globalProxy.Close()
	dir := t.TempDir()
	baseStore := sdkAuth.NewFileTokenStore()
	baseStore.SetBaseDir(dir)
	store := &flakyAntigravityHintStore{FileTokenStore: baseStore, firstSaveIn: make(chan struct{}), releaseSave: make(chan struct{})}
	manager := coreauth.NewManager(store, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := &config.Config{AuthDir: dir, SDKConfig: config.SDKConfig{ProxyURL: globalProxy.URL}}
	svc := &Service{cfg: cfg, coreManager: manager, antigravityContext: ctx}
	t.Cleanup(func() { cancel(); svc.WaitAntigravityProbes() })
	auth := antigravityTestAuth("persist-controls.json", "http://capability.invalid")
	auth.FileName = "persist-controls.json"
	auth.Metadata["type"] = "antigravity"
	auth.Metadata["base_url"] = "http://capability.invalid"
	auth.Attributes[coreauth.AttributePath] = filepath.Join(dir, auth.FileName)
	auth.ProxyURL = oldProxy.URL
	auth.Metadata["proxy_url"] = oldProxy.URL
	auth.Metadata["codex_identity_namespace"] = "old-namespace"
	auth, err := manager.Register(ctx, auth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	held, err := manager.AcquireCredentialConcurrency(ctx, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	store.blockFirst.Store(true)
	svc.registerModelsForAuth(ctx, auth)
	select {
	case <-store.firstSaveIn:
	case <-time.After(3 * time.Second):
		t.Fatal("background hint save did not enter")
	}
	updated, err := manager.UpdateAndPersistMetadata(ctx, auth.ID, func(a *coreauth.Auth) error {
		a.ProxyURL = newProxy.URL
		a.Metadata["proxy_url"] = newProxy.URL
		a.Metadata["access_token"] = "accepted-token"
		a.Metadata["codex_identity_namespace"] = "accepted-namespace"
		a.Metadata["concurrency_limit"] = 3
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.WaitAntigravityProbes()
	svc.registerModelsForAuth(ctx, updated)
	svc.WaitAntigravityProbes()
	current, _ := manager.GetByID(auth.ID)
	if current.CredentialVersion != updated.CredentialVersion || current.CredentialVersion != auth.CredentialVersion+1 || current.ProxyURL != newProxy.URL || current.Metadata["codex_identity_namespace"] != "accepted-namespace" {
		t.Fatal("hint persistence overwrote foreground controls or secret version")
	}
	if n, limit := manager.CredentialConcurrencySnapshot(auth.ID); n != 1 || limit != 3 {
		t.Fatalf("probe/save lease=%d/%d, want1/3", n, limit)
	}
	if oldCalls.Load() != 1 || newCalls.Load() != 1 || globalCalls.Load() != 0 {
		t.Fatalf("routes old/new/global=%d/%d/%d", oldCalls.Load(), newCalls.Load(), globalCalls.Load())
	}
	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3.1-flash-lite") || GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3-flash") {
		t.Fatal("new proxy capability catalog was not authoritative")
	}
	// Restore from the actual accepted remote-store snapshot, then reload the file store.
	if err := store.restoreRemoteToDisk(); err != nil {
		t.Fatal(err)
	}
	restarted := coreauth.NewManager(baseStore, nil, nil)
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, ok := restarted.GetByID(auth.ID)
	if !ok {
		t.Fatal("saved credential did not reload")
	}
	if loaded.ProxyURL != newProxy.URL || loaded.Metadata["access_token"] != "accepted-token" || loaded.Metadata["codex_identity_namespace"] != "accepted-namespace" || coreauth.EffectiveConcurrencyLimit(loaded) != 3 {
		t.Fatal("restart lost accepted operator controls")
	}
	resetAntigravityCapabilityCache()
	restartedService := &Service{cfg: cfg, coreManager: restarted}
	hints := restartedService.cachedAntigravityHints(loaded)
	if _, ok := hints.ModelIDs["gemini-3.1-flash-lite"]; !ok {
		t.Fatal("restart lost persisted model capability")
	}
	if _, stale := hints.ModelIDs["gemini-3-flash"]; stale {
		t.Fatal("restart restored obsolete proxy capability")
	}
}
