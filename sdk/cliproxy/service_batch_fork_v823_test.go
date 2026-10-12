package cliproxy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type batchStoreV823 struct {
	mu    sync.Mutex
	saved map[string]*coreauth.Auth
}

func (s *batchStoreV823) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (s *batchStoreV823) Save(_ context.Context, a *coreauth.Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saved == nil {
		s.saved = make(map[string]*coreauth.Auth)
	}
	s.saved[a.ID] = a.Clone()
	return a.ID, nil
}
func (s *batchStoreV823) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.saved, id)
	return nil
}

func TestV823BatchReconciliationKeepsLatestCredentialRevisionAndLease(t *testing.T) {
	for _, mode := range []string{"metadata", "reregister"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := &batchStoreV823{}
			manager := coreauth.NewManager(store, nil, nil)
			manager.RegisterExecutor(mockBatchTestExecutor{provider: "codex"})
			service := &Service{cfg: &config.Config{}, coreManager: manager}
			original, err := manager.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"}, Metadata: map[string]any{"access_token": "old-token", "codex_identity_namespace": "old-namespace"}})
			if err != nil {
				t.Fatal(err)
			}
			allowed, err := manager.Register(ctx, &coreauth.Auth{ID: t.Name() + "-allowed", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"}})
			if err != nil {
				t.Fatal(err)
			}
			reg := GlobalModelRegistry()
			t.Cleanup(func() {
				reg.UnregisterClient(original.ID)
				reg.UnregisterClient(allowed.ID)
				modelRegistrationTaskPostRunHook = nil
			})
			release, err := manager.AcquireCredentialConcurrency(ctx, original)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			var changed atomic.Bool
			var accepted *coreauth.Auth
			modelRegistrationTaskPostRunHook = func(id string) {
				if id != original.ID || !changed.CompareAndSwap(false, true) {
					return
				}
				edit := func(a *coreauth.Auth) error {
					a.ProxyURL = "socks5://accepted.invalid:1080"
					a.Metadata["access_token"] = "new-token"
					a.Metadata["codex_identity_namespace"] = "accepted-namespace"
					a.Metadata["concurrency_limit"] = 3
					a.Attributes["excluded_models"] = "gpt-6-astra,gpt-6.1-sol"
					return nil
				}
				var errUpdate error
				if mode == "metadata" {
					accepted, errUpdate = manager.UpdateAndPersistMetadata(ctx, id, edit)
				} else {
					manager.Remove(ctx, id)
					replacement := original.Clone()
					_ = edit(replacement)
					accepted, errUpdate = manager.Register(ctx, replacement)
				}
				if errUpdate != nil {
					t.Errorf("concurrent credential commit: %v", errUpdate)
				}
			}
			service.registerModelsForAuthBatch(ctx, manager.List())
			if !changed.Load() || accepted == nil {
				t.Fatal("post-registration update did not run")
			}
			current, ok := manager.GetByID(original.ID)
			if !ok {
				t.Fatal("current credential disappeared")
			}
			if current.ProxyURL != accepted.ProxyURL || current.Metadata["access_token"] != "new-token" || current.Metadata["codex_identity_namespace"] != "accepted-namespace" || current.CredentialVersion != accepted.CredentialVersion || current.RegistrationEpoch != accepted.RegistrationEpoch {
				t.Fatal("batch overwrote accepted credential controls or version")
			}
			if mode == "metadata" && accepted.CredentialVersion <= original.CredentialVersion {
				t.Fatal("metadata secret adoption did not advance version")
			}
			if mode == "reregister" && accepted.RegistrationEpoch <= original.RegistrationEpoch {
				t.Fatal("replacement epoch did not advance")
			}
			if n, limit := manager.CredentialConcurrencySnapshot(original.ID); n != 1 || limit != 3 {
				t.Fatalf("batch lease=%d/%d, want1/3", n, limit)
			}
			if reg.ClientSupportsModel(original.ID, "gpt-6-astra") || reg.ClientSupportsModel(original.ID, "gpt-6.1-sol") {
				t.Error("batch left excluded models registered after credential revision changed")
			}
			picked, err := manager.SelectAuth(ctx, "codex", "gpt-6-astra", coreexecutor.Options{})
			if err != nil || picked == nil || picked.ID != allowed.ID {
				t.Errorf("batch scheduler selected %v, error %v; want allowed sibling", picked, err)
			}
			store.mu.Lock()
			saved := store.saved[original.ID].Clone()
			store.mu.Unlock()
			if saved == nil || saved.ProxyURL != accepted.ProxyURL || saved.Metadata["access_token"] != "new-token" || saved.Metadata["codex_identity_namespace"] != "accepted-namespace" {
				t.Error("durable accepted metadata was lost")
			}
			release()
			if n, _ := manager.CredentialConcurrencySnapshot(original.ID); n != 0 {
				t.Errorf("held lease not released: %d", n)
			}
		})
	}
}
