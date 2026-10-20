package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV835PluginConcurrencySnapshotCallbacksRunOutsideManagerLock(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "single"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			m, _, auths, models := newCredentialConcurrencyManager(t, 2, 1)
			release, err := m.AcquireCredentialConcurrency(context.Background(), auths[0])
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			snapshots := 0
			scheduler := &fakePluginScheduler{handled: true, acrossPrioritiesFunc: func() bool {
				snapshots++
				if !m.mu.TryLock() {
					t.Fatal("plugin priority callback under manager lock")
				}
				m.mu.Unlock()
				return true
			}, pick: func(_ context.Context, request pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
				if !m.mu.TryLock() {
					t.Fatal("plugin selection callback under manager lock")
				}
				m.mu.Unlock()
				if len(request.Candidates) != 1 || request.Candidates[0].ID != auths[1].ID {
					t.Fatalf("full credential reached scheduler: %+v", request.Candidates)
				}
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: auths[1].ID}, true, nil
			}}
			m.SetPluginScheduler(scheduler)
			ctx := withCredentialConcurrencyAdmission(context.Background())
			var selected *Auth
			if mixed {
				selected, _, _, err = m.pickNextMixedLegacy(ctx, []string{"test"}, models[0], cliproxyexecutor.Options{}, nil)
			} else {
				selected, _, err = m.pickNextLegacy(ctx, "test", models[0], cliproxyexecutor.Options{}, nil)
			}
			if err != nil || selected == nil || selected.ID != auths[1].ID || snapshots != 1 {
				t.Fatalf("selection=%+v err=%v snapshots=%d", selected, err, snapshots)
			}
		})
	}
}
