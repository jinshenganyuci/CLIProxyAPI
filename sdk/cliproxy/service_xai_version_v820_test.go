package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestV820ServiceVersionUpdaterRebindsProxyUntilShutdown(t *testing.T) {
	var calls [2]atomic.Int32
	entered := make(chan int, 4)
	newProxy := func(index int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls[index].Add(1)
			if r.Method != http.MethodConnect || r.Host != "registry.npmjs.org:443" {
				t.Errorf("unexpected version route: %s %s", r.Method, r.Host)
			}
			// Refuse CONNECT so no request reaches the public npm registry.
			w.WriteHeader(http.StatusBadGateway)
			entered <- index
		}))
	}
	first, second := newProxy(0), newProxy(1)
	defer first.Close()
	defer second.Close()
	awaitProxy := func(index int) {
		t.Helper()
		select {
		case got := <-entered:
			if got != index {
				t.Fatalf("proxy index = %d, want %d", got, index)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("updater did not use proxy %d", index)
		}
	}
	fixture := func(name string) string {
		t.Helper()
		path, err := filepath.Abs(filepath.Join("..", "..", "internal", "registry", "models", name))
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{ProxyURL: first.URL},
		Models:    config.ModelCatalogs{Catalog: fixture("models.json"), CodexCatalog: fixture("codex_client_models.json"), DevinCatalog: fixture("devin_models.json")},
	}
	service := &Service{cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.startModelCatalogUpdaters(ctx)
	awaitProxy(0)
	updated := *cfg
	updated.ProxyURL = second.URL
	if service.commitConfigUpdate(&updated).cfg == nil {
		t.Fatal("proxy reload rejected")
	}
	awaitProxy(1)
	if service.xaiVersionContext != ctx || service.xaiVersionProxyURL != second.URL {
		t.Fatal("updater reload lost service lifetime or new proxy")
	}
	// A normalized no-op must not cause another start or cancel the live context.
	updated.ProxyURL = "  " + second.URL + "  "
	if service.commitConfigUpdate(&updated).cfg == nil {
		t.Fatal("neutral reload rejected")
	}
	if calls[0].Load() != 1 || calls[1].Load() != 1 {
		t.Fatalf("updater calls = %d/%d", calls[0].Load(), calls[1].Load())
	}
	cancel()
	updated.ProxyURL = first.URL
	if service.commitConfigUpdate(&updated).cfg == nil {
		t.Fatal("stopped service config rejected")
	}
	if service.xaiVersionProxyURL != second.URL {
		t.Fatal("stopped service restarted version updater")
	}
	// Starting again explicitly binds the new service lifetime and latest config.
	restarted, stopRestarted := context.WithCancel(context.Background())
	defer stopRestarted()
	service.startModelCatalogUpdaters(restarted)
	awaitProxy(0)
	if service.xaiVersionContext != restarted || calls[0].Load() != 2 || calls[1].Load() != 1 {
		t.Fatal("service restart did not rebind exactly once")
	}
}
