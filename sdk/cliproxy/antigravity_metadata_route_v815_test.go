package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityPersistedProxyEditCancelsAndReroutesProbeV815(t *testing.T) {
	for _, directFirst := range []bool{false, true} {
		for _, atomicStore := range []bool{false, true} {
			t.Run(fmt.Sprintf("direct-first=%t/atomic=%t", directFirst, atomicStore), func(t *testing.T) {
				resetAntigravityCapabilityCache()
				t.Cleanup(resetAntigravityCapabilityCache)
				started, cancelled := make(chan struct{}), make(chan struct{})
				var oldCalls, newCalls, globalCalls atomic.Int32
				oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					oldCalls.Add(1)
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					close(started)
					<-r.Context().Done()
					close(cancelled)
				}))
				newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					newCalls.Add(1)
					if r.URL.Path != antigravityModelsPath || r.Header.Get("Authorization") != "Bearer token-proxy-edit" {
						t.Error("account model probe lost its path or credential")
					}
					_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
				}))
				globalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					globalCalls.Add(1)
					http.Error(w, "unexpected global fallback", http.StatusBadGateway)
				}))
				ctx, cancel := context.WithCancel(t.Context())
				store := &cooldownProviderTokenStore{}
				svc := &Service{cfg: &config.Config{SDKConfig: config.SDKConfig{ProxyURL: globalServer.URL}}, coreManager: coreauth.NewManager(store, nil, nil)}
				t.Cleanup(func() {
					cancel()
					svc.WaitAntigravityProbes()
					oldServer.Close()
					newServer.Close()
					globalServer.Close()
				})
				auth := antigravityTestAuth("proxy-edit", newServer.URL)
				auth.ProxyURL = oldServer.URL
				wantProxy := "direct"
				if directFirst {
					auth.Attributes["base_url"] = oldServer.URL
					auth.ProxyURL = "direct"
					wantProxy = newServer.URL
				}
				auth, errRegister := svc.coreManager.Register(ctx, auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
				release, errAcquire := svc.coreManager.AcquireCredentialConcurrency(ctx, auth)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				t.Cleanup(release)
				svc.registerModelsForAuth(ctx, auth)
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("initial probe did not use its selected route")
				}
				edit := func(candidate *coreauth.Auth) error {
					candidate.ProxyURL = wantProxy
					candidate.Metadata["proxy_url"] = wantProxy
					candidate.Metadata["concurrency_limit"] = 3
					if atomicStore {
						return nil
					}
					_, errSave := store.Save(ctx, candidate)
					return errSave
				}
				var updated *coreauth.Auth
				var errEdit error
				if atomicStore {
					updated, errEdit = svc.coreManager.UpdateAndPersistMetadata(ctx, auth.ID, edit)
				} else {
					updated, errEdit = svc.coreManager.UpdatePersistedMetadata(ctx, auth.ID, edit)
				}
				if errEdit != nil {
					t.Fatal(errEdit)
				}
				// No service mutation callback is called: manager notification alone
				// must invalidate the old connected account snapshot.
				select {
				case <-cancelled:
				case <-time.After(3 * time.Second):
					t.Fatal("persisted proxy change did not cancel the old connected probe")
				}
				svc.WaitAntigravityProbes()
				svc.registerModelsForAuth(ctx, updated)
				svc.WaitAntigravityProbes()
				if oldCalls.Load() != 1 || newCalls.Load() != 1 || globalCalls.Load() != 0 {
					t.Fatalf("probe routes old/new/global = %d/%d/%d, want 1/1/0", oldCalls.Load(), newCalls.Load(), globalCalls.Load())
				}
				if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3.1-flash-lite") {
					t.Fatal("new selected route did not publish its entitlement")
				}
				if inflight, limit := svc.coreManager.CredentialConcurrencySnapshot(auth.ID); inflight != 1 || limit != 3 {
					t.Fatalf("probe/edit changed inference leases: %d/%d, want 1/3", inflight, limit)
				}
				antigravityCapabilityMu.RLock()
				failures := len(antigravityAuthFailureCache)
				antigravityCapabilityMu.RUnlock()
				if failures != 0 {
					t.Fatal("operator route edit was counted as upstream failure")
				}
			})
		}
	}
}
