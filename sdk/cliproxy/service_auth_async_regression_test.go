package cliproxy

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAuthRegistrationAsyncPluginPreservesNewerCredential(t *testing.T) {
	for _, responseAuth := range []bool{false, true} {
		t.Run(map[bool]string{false: "models_only", true: "auth_update"}[responseAuth], func(t *testing.T) {
			const authID = "async-plugin-canonical"
			manager := coreauth.NewManager(nil, nil, nil)
			service := &Service{cfg: &config.Config{}, coreManager: manager, pluginHost: pluginhost.New()}
			reg := GlobalModelRegistry()
			reg.UnregisterClient(authID)
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			ctx := coreauth.WithSkipPersist(context.Background())
			old := &coreauth.Auth{
				ID: authID, Provider: "codex", Status: coreauth.StatusActive,
				ProxyURL: "http://old-proxy.invalid:8080", Prefix: "old",
				Metadata:   map[string]any{"access_token": "old-token", "codex_identity_namespace": "old-namespace"},
				Attributes: map[string]string{"plan_type": "pro"},
			}
			started := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan struct{})
			var releaseOnce sync.Once
			oldHook := pluginModelsForAuth
			pluginModelsForAuth = func(_ context.Context, _ *pluginhost.Host, auth *coreauth.Auth) pluginhost.AuthModelResult {
				if auth.Prefix == "old" {
					close(started)
					<-release
					result := pluginhost.AuthModelResult{Handled: true, Provider: "codex", Models: []*ModelInfo{{ID: "stale-model"}}}
					if responseAuth {
						result.Auth = auth.Clone()
						result.Auth.Metadata["plugin_observed"] = "stale-result"
					}
					return result
				}
				return pluginhost.AuthModelResult{Handled: true, Provider: "codex", Models: []*ModelInfo{{ID: "current-model"}}}
			}
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				waitAsyncRegistration(t, finished, "old plugin completion")
				pluginModelsForAuth = oldHook
			})
			go func() {
				defer close(finished)
				service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: authID, Auth: old})
			}()
			waitAsyncRegistration(t, started, "blocked plugin model discovery")
			current, _ := manager.GetByID(authID)
			current.ProxyURL = "http://new-proxy.invalid:8080"
			current.Prefix = "new"
			current.Metadata["access_token"] = "new-token"
			current.Metadata["codex_identity_namespace"] = "new-namespace"
			service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authID, Auth: current})
			releaseOnce.Do(func() { close(release) })
			waitAsyncRegistration(t, finished, "old plugin completion")
			current, _ = manager.GetByID(authID)
			if current.ProxyURL != "http://new-proxy.invalid:8080" || current.Prefix != "new" || current.Metadata["access_token"] != "new-token" || current.Metadata["codex_identity_namespace"] != "new-namespace" {
				t.Fatal("stale plugin model discovery overwrote newer canonical credential controls")
			}
			models := reg.GetModelsForClient(authID)
			if len(models) == 0 {
				t.Fatal("newer credential has no registered models")
			}
			for _, model := range models {
				if strings.Contains(model.ID, "stale") || strings.HasPrefix(model.ID, "old/") {
					t.Fatalf("stale plugin models replaced newer registry: %s", model.ID)
				}
			}
		})
	}
}

func TestAuthRegistrationAsyncQueuedTaskDoesNotCrossRegistrationEpoch(t *testing.T) {
	const authID = "async-registration-epoch"
	reg := GlobalModelRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	ctx := coreauth.WithSkipPersist(context.Background())
	old := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Prefix: "old", Attributes: map[string]string{"plan_type": "pro"}}
	if _, errRegister := manager.Register(ctx, old); errRegister != nil {
		t.Fatal(errRegister)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var releaseOnce sync.Once
	var first atomic.Bool
	oldHook := modelRegistrationTaskHook
	modelRegistrationTaskHook = func() {
		if first.CompareAndSwap(false, true) {
			close(started)
			<-release
		}
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		waitAsyncRegistration(t, finished, "queued old epoch task")
		modelRegistrationTaskHook = oldHook
	})
	go func() {
		defer close(finished)
		service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authID, Auth: old})
	}()
	waitAsyncRegistration(t, started, "queued old epoch task")
	service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: authID})
	replacement := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Prefix: "new", Attributes: map[string]string{"plan_type": "pro"}}
	service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: authID, Auth: replacement})
	releaseOnce.Do(func() { close(release) })
	waitAsyncRegistration(t, finished, "queued old epoch task")
	models := reg.GetModelsForClient(authID)
	if len(models) == 0 {
		t.Fatal("replacement credential has no registered models")
	}
	for _, model := range models {
		if strings.HasPrefix(model.ID, "old/") {
			t.Fatalf("old registration epoch replaced current model prefix: %s", model.ID)
		}
	}
}

func TestAuthRegistrationAsyncNilRuntimeFinishesOwnDuplicateWait(t *testing.T) {
	for _, service := range []*Service{{}, {cfg: &config.Config{}}} {
		update := watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: "nil-runtime", Auth: &coreauth.Auth{ID: "nil-runtime", Provider: "codex"}}
		update.SetRevision(1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			service.handleAuthUpdates(context.Background(), []watcher.AuthUpdate{update, update})
		}()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			finishAuthRegistrations(service, []authRegistrationWait{{id: update.ID, ch: service.authRegistrationWaitCh(update.ID)}})
			waitAsyncRegistration(t, finished, "nil runtime cleanup")
			t.Fatal("nil runtime waited for its own duplicate registration before closing it")
		}
	}
}

type asyncRegistrationHook struct {
	coreauth.NoopHook
	onUpdated func(*coreauth.Auth)
}

func (h asyncRegistrationHook) OnAuthUpdated(_ context.Context, auth *coreauth.Auth) {
	h.onUpdated(auth)
}

func TestAuthRegistrationAsyncPluginHookDoesNotBlockOtherCredential(t *testing.T) {
	const authID = "async-plugin-slow-hook"
	const otherID = "async-plugin-other-auth"
	reg := GlobalModelRegistry()
	for _, id := range []string{authID, otherID} {
		reg.UnregisterClient(id)
	}
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		reg.UnregisterClient(otherID)
	})
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	otherFinished := make(chan struct{})
	var releaseOnce sync.Once
	hook := asyncRegistrationHook{onUpdated: func(auth *coreauth.Auth) {
		if auth.ID == authID && auth.Metadata["plugin_result"] == true {
			close(started)
			<-release
		}
	}}
	manager := coreauth.NewManager(nil, nil, hook)
	service := &Service{cfg: &config.Config{}, coreManager: manager, pluginHost: pluginhost.New()}
	ctx := coreauth.WithSkipPersist(context.Background())
	other := &coreauth.Auth{ID: otherID, Provider: "codex", Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(ctx, other); errRegister != nil {
		t.Fatal(errRegister)
	}
	oldHook := pluginModelsForAuth
	pluginModelsForAuth = func(_ context.Context, _ *pluginhost.Host, auth *coreauth.Auth) pluginhost.AuthModelResult {
		result := pluginhost.AuthModelResult{Handled: true, Provider: "codex", Models: []*ModelInfo{{ID: "discovered-model"}}}
		if auth.ID == authID {
			result.Auth = auth.Clone()
			result.Auth.Metadata = map[string]any{"plugin_result": true}
		}
		return result
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		waitAsyncRegistration(t, finished, "blocked plugin hook completion")
		pluginModelsForAuth = oldHook
	})
	go func() {
		defer close(finished)
		auth := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive}
		service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: authID, Auth: auth})
	}()
	waitAsyncRegistration(t, started, "blocked plugin update hook")
	go func() {
		defer close(otherFinished)
		other.ProxyURL = "http://other-proxy.invalid:8080"
		service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: otherID, Auth: other})
	}()
	select {
	case <-otherFinished:
	case <-time.After(2 * time.Second):
		releaseOnce.Do(func() { close(release) })
		waitAsyncRegistration(t, otherFinished, "unrelated credential cleanup")
		t.Fatal("plugin AuthUpdate hook held the service update lock and blocked another credential")
	}
	current, _ := manager.GetByID(otherID)
	if current.ProxyURL != "http://other-proxy.invalid:8080" {
		t.Fatal("unrelated credential edit did not take effect")
	}
}

func waitAsyncRegistration(t *testing.T, ch <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}
