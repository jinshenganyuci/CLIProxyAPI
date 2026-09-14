package auth

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMergeRefreshedAuthQuotaObservation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		updatedAt  time.Time
		updated    map[string]string
		currentAt  time.Time
		current    map[string]string
		staleEpoch bool
		wantAt     time.Time
		want       map[string]string
	}{
		{name: "new snapshot replaces stale keys", updatedAt: time.Unix(30, 0), updated: map[string]string{"daily": "75%"}, currentAt: time.Unix(20, 0), current: map[string]string{"daily": "50%", "stale": "value"}, wantAt: time.Unix(30, 0), want: map[string]string{"daily": "75%"}},
		{name: "newer concurrent observation wins", updatedAt: time.Unix(20, 0), updated: map[string]string{"daily": "75%"}, currentAt: time.Unix(30, 0), current: map[string]string{"daily": "60%", "weekly": "90%"}, wantAt: time.Unix(30, 0), want: map[string]string{"daily": "60%", "weekly": "90%"}},
		{name: "refresh without observation preserves current", updated: map[string]string{"daily": "75%"}, currentAt: time.Unix(20, 0), current: map[string]string{"daily": "50%"}, wantAt: time.Unix(20, 0), want: map[string]string{"daily": "50%"}},
		{name: "empty observed snapshot clears old signals", updatedAt: time.Unix(30, 0), currentAt: time.Unix(20, 0), current: map[string]string{"daily": "50%"}, wantAt: time.Unix(30, 0)},
		{name: "previous registration cannot update observation", updatedAt: time.Unix(30, 0), updated: map[string]string{"daily": "75%"}, currentAt: time.Unix(20, 0), current: map[string]string{"daily": "50%"}, staleEpoch: true, wantAt: time.Unix(20, 0), want: map[string]string{"daily": "50%"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &Auth{ID: "quota-observation", RegistrationEpoch: 1, Status: StatusActive}
			current := base.Clone()
			current.Quota = QuotaState{
				Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour), BackoffLevel: 4,
				ObservedAt: tc.currentAt, Signals: tc.current,
			}
			if tc.staleEpoch {
				current.RegistrationEpoch++
			}
			updated := base.Clone()
			updated.Quota = QuotaState{ObservedAt: tc.updatedAt, Signals: tc.updated}
			merged := MergeRefreshedAuth(base, current, updated)
			if merged == nil || !merged.Quota.ObservedAt.Equal(tc.wantAt) || !reflect.DeepEqual(merged.Quota.Signals, tc.want) {
				t.Fatalf("quota observation = %#v, want time %v signals %#v", merged, tc.wantAt, tc.want)
			}
			if !reflect.DeepEqual(cooldownFieldsOf(merged.Quota), cooldownFieldsOf(current.Quota)) || merged.RegistrationEpoch != current.RegistrationEpoch {
				t.Fatalf("refresh changed cooldown fields or registration: %#v", merged)
			}
			if len(merged.Quota.Signals) != 0 {
				merged.Quota.Signals["daily"] = "mutated clone"
				if current.Quota.Signals["daily"] == "mutated clone" || updated.Quota.Signals["daily"] == "mutated clone" {
					t.Fatal("merged observation aliases an input snapshot")
				}
			}
		})
	}
}

type quotaObservationRefreshExecutor struct {
	schedulerProviderTestExecutor
	observation QuotaState
	started     chan struct{}
	release     chan struct{}
}

func (e *quotaObservationRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
	}
	updated := auth.Clone()
	updated.Metadata["access_token"] = "fresh-token"
	updated.Quota = e.observation.Clone()
	return updated, nil
}

func TestManagerForceRefreshAuthQuotaObservation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cooldown     bool
		concurrentAt time.Time
		reregister   bool
	}{
		{name: "new observation and concurrent controls"},
		{name: "new observation during credential cooldown", cooldown: true},
		{name: "newer concurrent observation during credential cooldown", cooldown: true, concurrentAt: time.Unix(30, 0)},
		{name: "stale registration cannot publish observation", reregister: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			manager := NewManager(nil, nil, nil)
			executor := &quotaObservationRefreshExecutor{
				schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "devin"},
				observation:                   QuotaState{ObservedAt: time.Unix(20, 0), Signals: map[string]string{"daily": "75%", "weekly": "90%"}},
				started:                       make(chan struct{}), release: make(chan struct{}),
			}
			manager.RegisterExecutor(executor)
			registered, errRegister := manager.Register(ctx, &Auth{
				ID: "quota-refresh-" + tc.name, Provider: "devin", Status: StatusActive,
				ProxyURL: "http://old-proxy.invalid:8080", Prefix: "original",
				Metadata: map[string]any{"type": "devin", "access_token": "old-token", "refresh_token": "refresh-token", "proxy_url": "http://old-proxy.invalid:8080", "weight": float64(1)},
				Quota:    QuotaState{ObservedAt: time.Unix(10, 0), Signals: map[string]string{"daily": "50%", "stale": "value"}},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			type refreshResult struct {
				auth *Auth
				err  error
			}
			done := make(chan refreshResult, 1)
			go func() {
				auth, errRefresh := manager.ForceRefreshAuth(ctx, registered.ID)
				done <- refreshResult{auth: auth, err: errRefresh}
			}()
			<-executor.started
			current, ok := manager.GetByID(registered.ID)
			if !ok {
				t.Fatal("registered credential missing")
			}
			current.ProxyURL = "http://selected-proxy.invalid:8080"
			current.Metadata["proxy_url"] = current.ProxyURL
			current.Metadata["weight"] = float64(3)
			current.Metadata["concurrency"] = float64(2)
			current.Prefix = "edited"
			if !tc.concurrentAt.IsZero() {
				current.Quota.ObservedAt = tc.concurrentAt
				current.Quota.Signals = map[string]string{"daily": "60%", "concurrent": "newer"}
			}
			if tc.cooldown {
				current.Unavailable = true
				current.Status = StatusError
				current.StatusMessage = "active quota cooldown"
				current.NextRetryAfter = time.Now().Add(time.Hour)
				current.Quota.Exceeded = true
				current.Quota.Reason = "credential_quota"
				current.Quota.NextRecoverAt = current.NextRetryAfter
				current.Quota.BackoffLevel = 3
				current.LastError = &Error{HTTPStatus: 429, Message: "concurrent quota limit"}
			}
			var errUpdate error
			if tc.reregister {
				current.Metadata["access_token"] = "new-registration-token"
				current, errUpdate = manager.Register(ctx, current)
			} else {
				current, errUpdate = manager.Update(ctx, current)
			}
			if errUpdate != nil {
				t.Fatal(errUpdate)
			}
			close(executor.release)
			result := <-done
			if tc.reregister {
				if result.err == nil || !strings.Contains(result.err.Error(), "stale registration epoch") {
					t.Fatalf("old refresh error = %v, want stale registration epoch", result.err)
				}
			} else if result.err != nil {
				t.Fatal(result.err)
			}
			runtime, ok := manager.GetByID(registered.ID)
			if !ok || runtime == nil {
				t.Fatal("runtime credential missing after refresh")
			}
			wantQuota := executor.observation
			if tc.reregister || tc.concurrentAt.After(wantQuota.ObservedAt) {
				wantQuota = current.Quota
			}
			for _, got := range []*Auth{runtime, result.auth} {
				if got == nil && tc.reregister {
					continue
				}
				if got == nil || !got.Quota.ObservedAt.Equal(wantQuota.ObservedAt) || !reflect.DeepEqual(got.Quota.Signals, wantQuota.Signals) {
					t.Fatalf("runtime or returned observation = %#v, want %#v", got, wantQuota)
				}
				if !reflect.DeepEqual(cooldownFieldsOf(got.Quota), cooldownFieldsOf(current.Quota)) || got.RegistrationEpoch != current.RegistrationEpoch {
					t.Fatalf("refresh changed scheduler state or registration: %#v", got)
				}
				if got.ProxyURL != current.ProxyURL || got.Metadata["proxy_url"] != current.ProxyURL || got.Metadata["weight"] != float64(3) || got.Metadata["concurrency"] != float64(2) || got.Prefix != "edited" {
					t.Fatalf("refresh lost concurrent credential controls: %#v", got)
				}
				if tc.cooldown && (!got.Unavailable || got.Status != current.Status || got.StatusMessage != current.StatusMessage || !got.NextRetryAfter.Equal(current.NextRetryAfter) || !reflect.DeepEqual(got.LastError, current.LastError)) {
					t.Fatalf("refresh cleared active cooldown: %#v", got)
				}
				wantToken := "fresh-token"
				if tc.reregister {
					wantToken = "new-registration-token"
				}
				if got.Metadata["access_token"] != wantToken || got.Metadata["refresh_token"] != "refresh-token" {
					t.Fatalf("refresh changed credential content incorrectly: %#v", got.Metadata)
				}
				for _, key := range []string{"quota", "observed_at", "signals", "daily", "weekly"} {
					if _, exists := got.Metadata[key]; exists {
						t.Fatalf("passive quota %q leaked into token metadata", key)
					}
				}
			}
		})
	}
}

func TestManagerUpdateActiveQuotaObservation(t *testing.T) {
	for _, incomingAt := range []time.Time{{}, time.Unix(5, 0), time.Unix(20, 0)} {
		t.Run(incomingAt.String(), func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			recoverAt := time.Now().Add(time.Hour)
			registered, errRegister := manager.Register(ctx, &Auth{
				ID: "active-quota-update", Provider: "devin", Status: StatusError,
				Unavailable: true, NextRetryAfter: recoverAt,
				Quota: QuotaState{
					Exceeded: true, Reason: "credential_quota", NextRecoverAt: recoverAt, BackoffLevel: 3,
					ObservedAt: time.Unix(10, 0), Signals: map[string]string{"daily": "50%"},
				},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			incoming := registered.Clone()
			incoming.Quota = QuotaState{}
			if !incomingAt.IsZero() {
				incoming.Quota = QuotaState{ObservedAt: incomingAt, Signals: map[string]string{"weekly": "90%"}}
			}
			updated, errUpdate := manager.Update(ctx, incoming)
			if errUpdate != nil {
				t.Fatal(errUpdate)
			}
			want := registered.Quota
			if incomingAt.After(want.ObservedAt) {
				want = incoming.Quota
			}
			if !updated.Quota.ObservedAt.Equal(want.ObservedAt) || !reflect.DeepEqual(updated.Quota.Signals, want.Signals) {
				t.Fatalf("observation = %#v, want %#v", updated.Quota, want)
			}
			if !reflect.DeepEqual(cooldownFieldsOf(updated.Quota), cooldownFieldsOf(registered.Quota)) || !updated.Unavailable || !updated.NextRetryAfter.Equal(recoverAt) {
				t.Fatalf("ordinary update cleared active cooldown: %#v", updated.Quota)
			}
		})
	}
}
