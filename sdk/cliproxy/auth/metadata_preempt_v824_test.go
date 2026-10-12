package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
)

type metadataPreemptStoreV824 struct {
	*memoryAuthTestStore
	manager *Manager
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *metadataPreemptStoreV824) Save(ctx context.Context, a *Auth) (string, error) {
	s.manager.GetByID(a.ID)
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.gate }()
	if a.Metadata["antigravity_capability_hints"] != nil {
		s.once.Do(func() { close(s.entered) })
		<-ctx.Done()
		return "", ctx.Err()
	}
	return s.memoryAuthTestStore.Save(ctx, a)
}

type metadataPreemptHookV824 struct {
	NoopHook
	updated func()
}

func (h metadataPreemptHookV824) OnAuthUpdated(context.Context, *Auth) {
	if h.updated != nil {
		h.updated()
	}
}

func TestV824OperatorMetadataPreemptsBackgroundStoreWithoutLosingControls(t *testing.T) {
	for _, atomicEdit := range []bool{false, true} {
		for _, sameAuth := range []bool{false, true} {
			t.Run(fmt.Sprintf("atomic=%t/same=%t", atomicEdit, sameAuth), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					store := &metadataPreemptStoreV824{memoryAuthTestStore: newMemoryAuthTestStore(), gate: make(chan struct{}, 1), entered: make(chan struct{})}
					manager := NewManager(store, nil, nil)
					store.manager = manager
					auths := make(map[string]*Auth)
					for _, id := range []string{"background", "operator", "sibling"} {
						a, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "antigravity", Status: StatusActive, ProxyURL: "direct", Metadata: map[string]any{"access_token": "old-" + id, "project_id": "project", "codex_identity_namespace": "old-namespace"}})
						if err != nil {
							t.Fatal(err)
						}
						auths[id] = a
						release, err := manager.AcquireCredentialConcurrency(context.Background(), a)
						if err != nil {
							t.Fatal(err)
						}
						defer release()
					}
					manager.hook = metadataPreemptHookV824{updated: func() {
						state := manager.globalMetadataSaveState()
						state.mu.Lock()
						active := state.fgActive
						state.mu.Unlock()
						if active != 0 {
							t.Errorf("foreground save gate held during lifecycle hook: %d", active)
						}
					}}
					bgCtx, cancelBG := context.WithCancel(context.Background())
					defer cancelBG()
					bgDone := make(chan [2]bool, 1)
					go func() {
						saved, retry := manager.PersistMetadataEntry(bgCtx, "background", auths["background"].RegistrationEpoch, "antigravity_capability_hints", map[string]any{"model_ids": []any{"test-model"}}, nil)
						bgDone <- [2]bool{saved, retry}
					}()
					<-store.entered
					target := "operator"
					if sameAuth {
						target = "background"
					}
					done := make(chan error, 1)
					go func() {
						edit := func(a *Auth) error {
							a.ProxyURL = "socks5://selected.invalid:1080"
							a.Metadata["concurrency_limit"] = 3
							a.Metadata["codex_identity_namespace"] = "accepted-namespace"
							a.Metadata["access_token"] = "accepted-token"
							if atomicEdit {
								return nil
							}
							_, err := store.Save(context.Background(), a)
							return err
						}
						var err error
						if atomicEdit {
							_, err = manager.UpdateAndPersistMetadata(context.Background(), target, edit)
						} else {
							_, err = manager.UpdatePersistedMetadata(context.Background(), target, edit)
						}
						done <- err
					}()
					synctest.Wait()
					select {
					case err := <-done:
						if err != nil {
							t.Error(err)
						}
					default:
						t.Error("operator metadata failed to preempt background store I/O")
						cancelBG()
						if err := <-done; err != nil {
							t.Error(err)
						}
					}
					outcome := <-bgDone
					if outcome != [2]bool{false, true} {
						t.Errorf("background save result=%v, want canceled and retryable", outcome)
					}
					current, _ := manager.GetByID(target)
					if current.ProxyURL != "socks5://selected.invalid:1080" || current.Metadata["access_token"] != "accepted-token" || current.Metadata["codex_identity_namespace"] != "accepted-namespace" || current.Metadata["antigravity_capability_hints"] != nil || current.CredentialVersion != auths[target].CredentialVersion+1 {
						t.Fatal("foreground metadata or credential version lost")
					}
					for id := range auths {
						limit := 10
						if id == target {
							limit = 3
						}
						requireCredentialConcurrencySnapshot(t, manager, id, 1, limit)
					}
					saved, err := store.List(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, a := range saved {
						if a.ID == target {
							found = true
							if a.ProxyURL != current.ProxyURL || a.Metadata["access_token"] != "accepted-token" {
								t.Error("durable operator metadata lost")
							}
						}
					}
					if !found {
						t.Fatal("operator credential was not persisted")
					}
				})
			})
		}
	}
}
