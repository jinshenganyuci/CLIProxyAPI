package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPluginExcludedCandidatesPreserveCredentialConcurrency(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "single"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			manager, _, auths, models := newCredentialConcurrencyManager(t, 3, 1)
			releaseBusy, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), auths[0])
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer releaseBusy()
			plugin := &fakePluginScheduler{resp: pluginapi.SchedulerPickResponse{ExcludeCandidates: []string{auths[1].ID}}}
			manager.SetPluginScheduler(plugin)
			ctx := withCredentialConcurrencyAdmission(context.Background())
			pick := func() (*Auth, error) {
				if mixed {
					selected, _, _, errPick := manager.pickNextMixed(ctx, []string{"test"}, models[0], cliproxyexecutor.Options{}, nil)
					return selected, errPick
				}
				selected, _, errPick := manager.pickNext(ctx, "test", models[0], cliproxyexecutor.Options{}, nil)
				return selected, errPick
			}
			selected, errPick := pick()
			if errPick != nil || selected == nil || selected.ID != auths[2].ID {
				t.Fatalf("fallback selection = %v, %v; want the free non-excluded credential", selected, errPick)
			}
			for _, candidate := range plugin.requests[0].Candidates {
				if candidate.ID == auths[0].ID {
					t.Fatal("full credential reached plugin selection")
				}
			}
			releaseLast, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), auths[2])
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer releaseLast()
			selected, errPick = pick()
			if selected != nil || !IsCredentialConcurrencyError(errPick) {
				t.Fatalf("full remaining candidates = %v, %v; want local concurrency rejection", selected, errPick)
			}
			for _, candidate := range auths {
				current, _ := manager.GetByID(candidate.ID)
				if current.Unavailable || current.LastError != nil || current.Quota.Exceeded || !current.NextRetryAfter.IsZero() {
					t.Fatal("local fallback rejection changed credential health")
				}
			}
			releaseLast()
			selected, errPick = pick()
			if errPick != nil || selected == nil || selected.ID != auths[2].ID {
				t.Fatalf("released fallback did not recover: %v, %v", selected, errPick)
			}
		})
	}
}

func TestCredentialConcurrencyNeverInheritsPoolCooldownAnnotation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Unix(1800000000, 0)
	busy := newCredentialConcurrencyError()
	got := manager.annotatePoolCooldownTerminalErrorAt(context.Background(), []string{"test"}, "model", cliproxyexecutor.Options{}, busy, now.Add(time.Hour), now)
	if got != busy || !IsCredentialConcurrencyError(got) {
		t.Fatalf("local admission was replaced by a pool error: %T %v", got, got)
	}
	if HasTrustedRetryDeadline(got) || SafeResponseHeaders(got).Get("Retry-After") != "" {
		t.Fatal("local admission inherited an upstream recovery deadline")
	}
}
