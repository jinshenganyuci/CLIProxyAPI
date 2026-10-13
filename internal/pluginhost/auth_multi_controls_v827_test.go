package pluginhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func multiControlsHostV827(t *testing.T, parsed *[]pluginapi.AuthData) (*Host, *coreauth.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	host := newHostWithRecords(capabilityRecord{id: "multi-controls", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: fakeAuthProvider{identifier: "codex", parseAuth: func(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
		return pluginapi.AuthParseResponse{Handled: true, Auths: *parsed}, nil
	}}}}})
	host.runtimeConfig = &config.Config{AuthDir: dir}
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)
	return host, manager, dir
}
func saveMultiControlsV827(host *Host, metadata map[string]any) error {
	data, _ := json.Marshal(metadata)
	req, _ := json.Marshal(pluginapi.HostAuthSaveRequest{Name: "multi.json", JSON: data})
	_, err := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, req)
	return err
}
func TestV827PluginExpandedAuthKeepsPerCredentialControlsAndLeases(t *testing.T) {
	ctx := context.Background()
	var selectedHits, directHits, parentHits, globalHits atomic.Int32
	server := func(counter *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { counter.Add(1); _, _ = w.Write([]byte("isolated-route")) }))
	}
	selected := server(&selectedHits)
	defer selected.Close()
	origin := server(&directHits)
	defer origin.Close()
	parent := server(&parentHits)
	defer parent.Close()
	global := server(&globalHits)
	defer global.Close()
	parsed := []pluginapi.AuthData{{Provider: "codex", ID: "multi-a", Metadata: map[string]any{"proxy_url": selected.URL, "concurrency_limit": 3}}, {Provider: "codex", ID: "multi-b", ProxyURL: "direct", Metadata: map[string]any{"concurrency_limit": 2}}, {Provider: "codex", ID: "multi-c", Metadata: map[string]any{}}}
	host, manager, _ := multiControlsHostV827(t, &parsed)
	host.runtimeConfig.ProxyURL = global.URL
	metadata := map[string]any{"type": "codex", "proxy_url": parent.URL, codex.CredentialIdentityVersionMetadataKey: 1, codex.CredentialIdentityNamespaceMetadataKey: "source-stable-namespace"}
	if err := saveMultiControlsV827(host, metadata); err != nil {
		t.Fatal(err)
	}
	var releases []func()
	indexes := make(map[string]string)
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i, tc := range []struct {
		id, proxy string
		limit     int
	}{{"multi-a", selected.URL, 3}, {"multi-b", "direct", 2}, {"multi-c", parent.URL, 10}} {
		a, ok := manager.GetByID(tc.id)
		if !ok {
			t.Fatal("missing expanded credential")
		}
		if a.ProxyURL != tc.proxy {
			t.Errorf("credential %s proxy=%q, want %q", tc.id, a.ProxyURL, tc.proxy)
		}
		if previous, exists := indexes[a.EnsureIndex()]; exists || a.Index == "" {
			t.Fatalf("expanded credential index collides: %s and %s", previous, tc.id)
		}
		indexes[a.Index] = tc.id
		if a.Metadata[codex.CredentialIdentityNamespaceMetadataKey] != "source-stable-namespace" {
			t.Errorf("credential %s lost file identity", tc.id)
		}
		if coreauth.EffectiveConcurrencyLimit(a) != tc.limit {
			t.Errorf("credential %s lost configured limit", tc.id)
		}
		resp, err := host.newHTTPClient(a).Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: origin.URL})
		if err != nil || string(resp.Body) != "isolated-route" {
			t.Fatalf("route %d: %v", i, err)
		}
		release, err := manager.AcquireCredentialConcurrency(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if selectedHits.Load() != 1 || directHits.Load() != 1 || parentHits.Load() != 1 || globalHits.Load() != 0 {
		t.Errorf("route hits selected/direct/parent/global=%d/%d/%d/%d", selectedHits.Load(), directHits.Load(), parentHits.Load(), globalHits.Load())
	}
	metadata["concurrency_limit"] = 4
	if err := saveMultiControlsV827(host, metadata); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"multi-a", "multi-b", "multi-c"} {
		if n, limit := manager.CredentialConcurrencySnapshot(id); n != 1 || limit != 4 {
			t.Errorf("source limit not preserved for %s: %d/%d", id, n, limit)
		}
	}
	metadata["concurrency_limit"] = nil
	if err := saveMultiControlsV827(host, metadata); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"multi-a", "multi-b", "multi-c"} {
		if n, limit := manager.CredentialConcurrencySnapshot(id); n != 1 || limit != 10 {
			t.Errorf("null source limit failed for %s: %d/%d", id, n, limit)
		}
	}
	for _, release := range releases {
		release()
	}
	for _, id := range []string{"multi-a", "multi-b", "multi-c"} {
		if n, _ := manager.CredentialConcurrencySnapshot(id); n != 0 {
			t.Fatal("expanded credential lease leaked")
		}
	}
}
func TestV827PluginAuthSaveRejectsAllInvalidLimitsBeforeFileOrManagerMutation(t *testing.T) {
	for _, where := range []string{"source", "child"} {
		t.Run(where, func(t *testing.T) {
			parsed := []pluginapi.AuthData{{Provider: "codex", ID: "valid-first"}, {Provider: "codex", ID: "invalid-second"}}
			metadata := map[string]any{"type": "codex"}
			if where == "source" {
				metadata["concurrency_limit"] = 0
			} else {
				parsed[1].Metadata = map[string]any{"concurrency_limit": 0}
			}
			host, manager, dir := multiControlsHostV827(t, &parsed)
			if err := saveMultiControlsV827(host, metadata); err == nil {
				t.Error("invalid limit accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, "multi.json")); !os.IsNotExist(err) {
				t.Errorf("invalid batch mutated disk: %v", err)
			}
			if len(manager.List()) != 0 {
				t.Error("invalid batch partially mutated manager")
			}
		})
	}
}
