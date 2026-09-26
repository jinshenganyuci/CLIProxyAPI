package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRefreshRecoveryResetsBackoffWithoutChangingForkControlsUpgrade7318(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	executor := &mockOAuthErrorExecutor{id: "codex", errToReturn: errors.New("invalid_grant: synthetic revoked token")}
	manager.RegisterExecutor(executor)
	original := &Auth{
		ID: "backoff-upgrade", Provider: "codex", Status: StatusActive, ProxyURL: "socks5://selected.invalid:1080",
		Metadata: map[string]any{
			"access_token": "expired", "refresh_token": "synthetic-refresh", "expired": "2000-01-01T00:00:00Z",
			"proxy_url": "socks5://selected.invalid:1080", "codex_identity_version": 1,
			"codex_identity_namespace": "d9d11681-7463-4840-a8da-61e437176f2f", "priority": 7,
		},
		Attributes: map[string]string{"priority": "7", AttributeFilePriority: "true", AttributeSourceBackend: AuthSourceFile},
	}
	registered, errRegister := manager.Register(ctx, original)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	release, errAcquire := manager.AcquireCredentialConcurrency(ctx, registered)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	defer release()
	for attempt := 1; attempt <= 2; attempt++ {
		if _, errRefresh := manager.ForceRefreshAuth(ctx, original.ID); errRefresh == nil {
			t.Fatal("synthetic invalid_grant was ignored")
		}
	}
	executor.errToReturn = nil
	recovered, errRefresh := manager.ForceRefreshAuth(ctx, original.ID)
	if errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if recovered.RefreshFailures != 0 {
		t.Fatalf("successful refresh retained %d failures; next invalid grant would inherit stale backoff", recovered.RefreshFailures)
	}
	if recovered.ProxyURL != original.ProxyURL || recovered.Metadata["proxy_url"] != original.ProxyURL || recovered.Metadata["codex_identity_namespace"] != original.Metadata["codex_identity_namespace"] || recovered.Attributes["priority"] != "7" {
		t.Fatal("refresh recovery changed proxy, identity, or file priority")
	}
	if inFlight, limit := manager.CredentialConcurrencySnapshot(original.ID); inFlight != 1 || limit != 10 {
		t.Fatalf("refresh changed the existing lease or default concurrency: %d/%d", inFlight, limit)
	}
	executor.errToReturn = errors.New("invalid_grant: synthetic next failure")
	before := time.Now()
	_, _ = manager.ForceRefreshAuth(ctx, original.ID)
	current, _ := manager.GetByID(original.ID)
	if current.RefreshFailures != 1 || current.NextRefreshAfter.Before(before.Add(time.Minute)) || current.NextRefreshAfter.After(time.Now().Add(time.Minute)) {
		t.Fatal("new failure did not restart at the native one-minute backoff")
	}
}

func TestRefreshFailureCounterThreeWayMergeUpgrade7318(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		name := "successful_reset"
		if concurrent {
			name = "preserve_concurrent_failure"
		}
		t.Run(name, func(t *testing.T) {
			base := &Auth{ID: "counter-merge", RefreshFailures: 2, Status: StatusError}
			current, updated := base.Clone(), base.Clone()
			updated.RefreshFailures = 0
			updated.Status = StatusActive
			want := 0
			if concurrent {
				current.RefreshFailures = 3
				want = 3
			}
			if got := MergeRefreshedAuth(base, current, updated).RefreshFailures; got != want {
				t.Fatalf("merged refresh failures = %d, want %d", got, want)
			}
		})
	}
}

func TestDisabledInvalidGrantUnschedulesDirtyRefreshUpgrade7318(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	loop := newAuthAutoRefreshLoop(manager, time.Second, 1)
	loop.queueReschedule("disabled")
	loop.remove("disabled")
	if _, exists := loop.dirty["disabled"]; exists {
		t.Fatal("removed credential remains in pending refresh work")
	}
	for _, disabled := range []*Auth{
		{Disabled: true, LastError: &Error{Message: "invalid_grant: revoked"}},
		{Status: StatusDisabled, LastError: &Error{Code: "invalid_grant"}},
	} {
		if _, scheduled := nextRefreshCheckAt(time.Now(), disabled, time.Second); scheduled || manager.shouldRefresh(disabled, time.Now()) {
			t.Fatal("disabled invalid_grant credential remained eligible for refresh")
		}
	}
	for failures, want := range map[int]time.Duration{0: time.Minute, 1: time.Minute, 2: 2 * time.Minute, 5: 16 * time.Minute, 6: 30 * time.Minute, 1000: 30 * time.Minute} {
		if got := invalidGrantBackoffDuration(failures); got != want {
			t.Fatalf("backoff(%d) = %v, want %v", failures, got, want)
		}
	}
}
