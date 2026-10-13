package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestV827QuotaAffinityRespectsConcurrencyAndPinnedCredentials(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, mode := range []string{SessionAffinityRebindOnAny, SessionAffinityRebindOnQuota} {
			t.Run(fmt.Sprintf("mixed=%t/mode=%s", mixed, mode), func(t *testing.T) {
				ctx := withCredentialConcurrencyAdmission(context.Background())
				provider := fmt.Sprintf("affinity-control-%t-%s", mixed, mode)
				model := "v827-model"
				selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &FillFirstSelector{}, TTL: time.Hour, RebindOn: mode})
				defer selector.Stop()
				m := NewManager(nil, selector, nil)
				m.RegisterExecutor(schedulerTestExecutor{provider: provider})
				var auths []*Auth
				for i, limit := range []int{3, 2, 10} {
					a, err := m.Register(ctx, &Auth{ID: fmt.Sprintf("%s-%d", provider, i), Provider: provider, Status: StatusActive, ProxyURL: "direct", Attributes: map[string]string{"priority": fmt.Sprint(3 - i)}, Metadata: map[string]any{"concurrency_limit": limit}})
					if err != nil {
						t.Fatal(err)
					}
					auths = append(auths, a)
					registry.GetGlobalRegistry().RegisterClient(a.ID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				}
				pick := func(opts coreexecutor.Options) (*Auth, error) {
					if mixed {
						a, _, _, err := m.pickNextMixed(ctx, []string{provider}, model, opts, nil)
						return a, err
					}
					a, _, err := m.pickNext(ctx, provider, model, opts, nil)
					return a, err
				}
				opts := coreexecutor.Options{Metadata: map[string]any{coreexecutor.DerivedSessionIDMetadataKey: "stable-affinity"}}
				first, err := pick(opts)
				if err != nil || first.ID != auths[0].ID {
					t.Fatalf("initial selection=%v %v", first, err)
				}
				var releases []func()
				defer func() {
					for _, release := range releases {
						release()
					}
				}()
				for range 3 {
					release, err := m.AcquireCredentialConcurrency(ctx, auths[0])
					if err != nil {
						t.Fatal(err)
					}
					releases = append(releases, release)
				}
				selected, err := pick(opts)
				if err != nil || selected.ID != auths[1].ID {
					t.Fatalf("full bound credential escaped capacity filter: %v %v", selected, err)
				}
				pinned := coreexecutor.Options{Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: auths[0].ID, coreexecutor.DerivedSessionIDMetadataKey: "strict-pinned"}}
				if _, errPin := pick(pinned); !IsCredentialConcurrencyError(errPin) {
					t.Fatalf("pinned busy credential escaped: %v", errPin)
				}
				if n, limit := m.CredentialConcurrencySnapshot(auths[0].ID); n != 3 || limit != 3 {
					t.Fatal("selection changed occupied budget")
				}
				for _, release := range releases {
					release()
				}
				recovered, err := pick(opts)
				want := auths[1].ID
				if mode == SessionAffinityRebindOnQuota {
					want = auths[0].ID
				}
				if err != nil || recovered.ID != want {
					t.Fatalf("affinity recovery=%v %v, want %s", recovered, err, want)
				}
				for _, a := range auths {
					if n, _ := m.CredentialConcurrencySnapshot(a.ID); n != 0 {
						t.Fatal("selection leaked or stole a lease")
					}
				}
			})
		}
	}
}

type sharedSessionsV827 struct {
	mu       sync.Mutex
	releases map[string]func()
	closed   []string
}
type sharedExecutorV827 struct {
	replaceAwareExecutor
	store *sharedSessionsV827
}

func (e *sharedExecutorV827) SharesExecutionSessionsWith(other ProviderExecutor) bool {
	o, ok := other.(*sharedExecutorV827)
	return ok && o.store == e.store
}
func (e *sharedExecutorV827) CloseAuthExecutionSessions(id string) {
	e.store.mu.Lock()
	release := e.store.releases[id]
	delete(e.store.releases, id)
	e.store.closed = append(e.store.closed, id)
	e.store.mu.Unlock()
	if release != nil {
		release()
	}
}
func (e *sharedExecutorV827) CloseExecutionSession(id string) {
	if id != CloseAllExecutionSessionsID {
		return
	}
	e.store.mu.Lock()
	releases := e.store.releases
	e.store.releases = make(map[string]func())
	e.store.closed = append(e.store.closed, id)
	e.store.mu.Unlock()
	for _, release := range releases {
		release()
	}
}
func TestV827SharedExecutorReloadAndRemovalPreserveSiblingLeases(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	store := &sharedSessionsV827{releases: make(map[string]func())}
	first := &sharedExecutorV827{replaceAwareExecutor: replaceAwareExecutor{id: "codex"}, store: store}
	m.RegisterExecutor(first)
	var auths []*Auth
	for i, limit := range []int{3, 2, 10} {
		a, err := m.Register(ctx, &Auth{ID: fmt.Sprintf("session-%d", i), Provider: "codex", Status: StatusActive, ProxyURL: "direct", Metadata: map[string]any{"concurrency_limit": limit, "codex_identity_namespace": "fixed"}})
		if err != nil {
			t.Fatal(err)
		}
		auths = append(auths, a)
		release, err := m.AcquireCredentialConcurrency(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		store.releases[a.ID] = release
	}
	replacement := &sharedExecutorV827{replaceAwareExecutor: replaceAwareExecutor{id: "codex"}, store: store}
	m.RegisterExecutor(replacement)
	if len(store.closed) != 0 {
		t.Fatal("shared-store replacement closed live sessions")
	}
	for _, a := range auths {
		if n, _ := m.CredentialConcurrencySnapshot(a.ID); n != 1 {
			t.Fatal("reload released an occupied lease")
		}
	}
	m.Remove(ctx, auths[0].ID)
	if len(store.closed) != 1 || store.closed[0] != auths[0].ID {
		t.Fatalf("removal scope=%v", store.closed)
	}
	if n, _ := m.CredentialConcurrencySnapshot(auths[0].ID); n != 0 {
		t.Fatal("removed credential session did not release")
	}
	for _, a := range auths[1:] {
		if n, _ := m.CredentialConcurrencySnapshot(a.ID); n != 1 {
			t.Fatal("removed credential closed a sibling")
		}
	}
	previousEpoch := auths[0].RegistrationEpoch
	registered, err := m.Register(ctx, auths[0].Clone())
	if err != nil {
		t.Fatal(err)
	}
	if registered.RegistrationEpoch <= previousEpoch {
		t.Fatal("re-registration reused prior epoch")
	}
	other := &sharedExecutorV827{replaceAwareExecutor: replaceAwareExecutor{id: "codex"}, store: &sharedSessionsV827{releases: make(map[string]func())}}
	m.RegisterExecutor(other)
	if len(store.closed) != 2 || store.closed[1] != CloseAllExecutionSessionsID {
		t.Fatal("unshared replacement did not close retired sessions")
	}
	for _, a := range auths {
		if n, _ := m.CredentialConcurrencySnapshot(a.ID); n != 0 {
			t.Fatal("retired session leaked a lease")
		}
	}
}
