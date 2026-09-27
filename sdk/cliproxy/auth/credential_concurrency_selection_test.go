package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type specialConcurrencySelection func(context.Context, *Manager, cliproxyexecutor.Options) (*Auth, func(), error)

func specialConcurrencySelectors() map[string]specialConcurrencySelection {
	return map[string]specialConcurrencySelection{
		"live_kind": func(ctx context.Context, m *Manager, opts cliproxyexecutor.Options) (*Auth, func(), error) {
			return m.SelectAuthByKindWithConcurrency(ctx, "codex", "", AuthKindOAuth, opts)
		},
		"alpha_policy": func(ctx context.Context, m *Manager, opts cliproxyexecutor.Options) (*Auth, func(), error) {
			return m.SelectAuthWithCredentialPolicyAndConcurrency(ctx, "codex", "", CredentialPolicyCodexAlphaSearchV1, opts)
		},
	}
}

func newSpecialConcurrencyManager(t *testing.T) (*Manager, *Auth, *Auth) {
	t.Helper()
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	register := func(id string) *Auth {
		credential, errRegister := manager.Register(context.Background(), &Auth{
			ID: id, Provider: "codex", Status: StatusActive,
			Metadata: map[string]any{"access_token": "synthetic-token", "concurrency_limit": 1},
		})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		return credential
	}
	return manager, register("special-a"), register("special-b")
}

func TestSpecialCredentialConcurrencySelectionFallbackAndControl(t *testing.T) {
	for name, selectAuth := range specialConcurrencySelectors() {
		t.Run(name, func(t *testing.T) {
			manager, first, second := newSpecialConcurrencyManager(t)
			releaseFirst, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), first)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer releaseFirst()
			selected, releaseSecond, errSelect := selectAuth(nil, manager, cliproxyexecutor.Options{})
			if errSelect != nil || selected == nil || selected.ID != second.ID {
				t.Fatalf("selection = %v, %v; want available credential %s", selected, errSelect, second.ID)
			}
			defer releaseSecond()
			if inflight, _ := manager.CredentialConcurrencySnapshot(second.ID); inflight != 1 {
				t.Fatalf("selected credential inflight = %d", inflight)
			}
			if _, _, errBusy := selectAuth(context.Background(), manager, cliproxyexecutor.Options{}); !IsCredentialConcurrencyError(errBusy) {
				t.Fatalf("all busy error = %v", errBusy)
			}
			pinned := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: first.ID}}
			if _, _, errPinned := selectAuth(context.Background(), manager, pinned); !IsCredentialConcurrencyError(errPinned) {
				t.Fatalf("pinned busy execution error = %v", errPinned)
			}
			control, errControl := manager.SelectAuthByKind(context.Background(), "codex", "", AuthKindOAuth, pinned)
			if errControl != nil || control == nil || control.ID != first.ID {
				t.Fatalf("existing session control rejected while full: %v, %v", control, errControl)
			}
			releaseSecond()
			if inflight, _ := manager.CredentialConcurrencySnapshot(second.ID); inflight != 0 {
				t.Fatalf("released credential inflight = %d", inflight)
			}
		})
	}
}

func TestSpecialCredentialConcurrencyPluginPrefiltersAndDoesNotRetryRace(t *testing.T) {
	for name, selectAuth := range specialConcurrencySelectors() {
		t.Run(name, func(t *testing.T) {
			manager, first, second := newSpecialConcurrencyManager(t)
			releaseFirst, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), first)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer releaseFirst()
			var releaseRacing func()
			plugin := &fakePluginScheduler{pick: func(ctx context.Context, request pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
				if len(request.Candidates) != 1 || request.Candidates[0].ID != second.ID {
					t.Fatalf("plugin candidates contain occupied credentials: %#v", request.Candidates)
				}
				var errRace error
				releaseRacing, errRace = manager.AcquireCredentialConcurrency(ctx, second)
				if errRace != nil {
					t.Fatal(errRace)
				}
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: second.ID}, true, nil
			}}
			manager.SetPluginScheduler(plugin)
			_, release, errSelect := selectAuth(context.Background(), manager, cliproxyexecutor.Options{})
			if release != nil {
				release()
			}
			if releaseRacing != nil {
				defer releaseRacing()
			}
			if !IsCredentialConcurrencyError(errSelect) || plugin.calls != 1 {
				t.Fatalf("admission race error/plugin calls = %v/%d, want busy/1", errSelect, plugin.calls)
			}
		})
	}
}

func TestSpecialCredentialConcurrencyPreservesPluginDenial(t *testing.T) {
	for name, selectAuth := range specialConcurrencySelectors() {
		t.Run(name, func(t *testing.T) {
			manager, first, second := newSpecialConcurrencyManager(t)
			for _, credential := range []*Auth{first, second} {
				release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), credential)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				defer release()
			}
			denied := &Error{Code: "access_denied", Message: "synthetic plugin denial", HTTPStatus: http.StatusForbidden}
			plugin := &fakePluginScheduler{err: denied}
			manager.SetPluginScheduler(plugin)
			_, _, errSelect := selectAuth(context.Background(), manager, cliproxyexecutor.Options{})
			if !errors.Is(errSelect, denied) || plugin.calls != 1 {
				t.Fatalf("plugin denial/calls = %v/%d, want original denial/1", errSelect, plugin.calls)
			}
		})
	}
}

func TestSpecialCredentialConcurrencyRefusesLocalSelectionInHomeMode(t *testing.T) {
	for name, selectAuth := range specialConcurrencySelectors() {
		t.Run(name, func(t *testing.T) {
			manager, first, _ := newSpecialConcurrencyManager(t)
			manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
			_, _, errSelect := selectAuth(context.Background(), manager, cliproxyexecutor.Options{})
			var authErr *Error
			if !errors.As(errSelect, &authErr) || authErr.Code != "home_unavailable" {
				t.Fatalf("Home mode local selection error = %v", errSelect)
			}
			if inflight, _ := manager.CredentialConcurrencySnapshot(first.ID); inflight != 0 {
				t.Fatalf("Home mode consumed local slots: %d", inflight)
			}
		})
	}
}
