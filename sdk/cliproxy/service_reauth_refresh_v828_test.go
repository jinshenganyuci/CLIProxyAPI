package cliproxy

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type reauthRefreshExecutorV828 struct {
	syncTestExecutor
	calls atomic.Int64
}

func (e *reauthRefreshExecutorV828) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.calls.Add(1)
	next := auth.Clone()
	expiry := time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	if siwc, ok := next.Metadata["siwc"].(map[string]any); ok {
		siwc["access_token"] = "fresh-siwc-refreshed"
		siwc["expired"] = expiry
	} else {
		next.Metadata["access_token"] = "fresh-refreshed"
		next.Metadata["expired"] = expiry
	}
	return next, nil
}

type reauthRefreshLeadV828 struct{}

func (reauthRefreshLeadV828) RefreshLead() *time.Duration { lead := 24 * time.Hour; return &lead }

func TestV828CredentialReplacementReschedulesRefreshWithoutLosingControls(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		replace, siwc, disabled bool
		backoff                 time.Duration
	}{
		{name: "control_only", backoff: time.Minute},
		{name: "control_only_failure_backoff", backoff: 5 * time.Minute},
		{name: "replacement_tokens", replace: true, backoff: time.Minute},
		{name: "siwc_only_replacement", siwc: true, backoff: time.Minute},
		{name: "disabled_replacement", replace: true, disabled: true, backoff: time.Minute},
	} {
		replace := tc.replace || tc.siwc
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				now := time.Now()
				m := coreauth.NewManager(nil, nil, nil)
				before, err := m.Register(ctx, &coreauth.Auth{ID: "reauth-v828", Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Runtime: reauthRefreshLeadV828{},
					LastRefreshedAt: now.Add(-time.Hour), NextRefreshAfter: now.Add(tc.backoff),
					Quota:       coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour)},
					Metadata:    map[string]any{"type": "codex", "access_token": "old-access", "refresh_token": "old-refresh", "expired": now.Add(7 * 24 * time.Hour).Format(time.RFC3339), "proxy_url": "direct", "concurrency_limit": 3, "codex_identity_namespace": "stable-namespace"},
					ModelStates: map[string]*coreauth.ModelState{"old-model": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour), LastError: &coreauth.Error{HTTPStatus: 503, Message: "existing-model-failure"}}}})
				if err != nil {
					t.Fatal(err)
				}
				if tc.siwc {
					before.Metadata["use_siwc"] = true
					before.Metadata["siwc"] = map[string]any{"access_token": "old-siwc", "refresh_token": "old-siwc-refresh", "client_id": "client", "expired": now.Add(7 * 24 * time.Hour).Format(time.RFC3339)}
					before, err = m.Update(ctx, before)
					if err != nil {
						t.Fatal(err)
					}
				}
				release, err := m.AcquireCredentialConcurrency(ctx, before)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				incoming := before.Clone()
				incoming.Generation = 0
				incoming.LastRefreshedAt = now
				incoming.NextRefreshAfter = time.Time{}
				incoming.ProxyURL = "http://selected.invalid:8123"
				incoming.Metadata["proxy_url"] = incoming.ProxyURL
				incoming.ModelStates = nil
				incoming.Quota = coreauth.QuotaState{}
				if tc.disabled {
					incoming.Disabled = true
					incoming.Status = coreauth.StatusDisabled
				}
				if tc.replace {
					incoming.Metadata["access_token"] = "fresh-access"
					incoming.Metadata["refresh_token"] = "fresh-refresh"
					incoming.Metadata["expired"] = now.Add(time.Hour).Format(time.RFC3339)
				}
				if tc.siwc {
					incoming.Metadata["use_siwc"] = true
					incoming.Metadata["siwc"] = map[string]any{"access_token": "fresh-siwc", "refresh_token": "fresh-siwc-refresh", "client_id": "client", "expired": now.Add(time.Minute).Format(time.RFC3339)}
				}
				service := &Service{cfg: &config.Config{}, coreManager: m}
				service.prepareCoreAuthForModelRegistration(ctx, incoming)
				got, ok := m.GetByID(before.ID)
				if !ok {
					t.Fatal("credential disappeared")
				}
				if replace {
					if !got.NextRefreshAfter.IsZero() || !got.LastRefreshedAt.Equal(now) {
						t.Errorf("new credentials inherited old refresh gate: next=%v, last=%v", got.NextRefreshAfter, got.LastRefreshedAt)
					}
				} else if !got.NextRefreshAfter.Equal(before.NextRefreshAfter) || !got.LastRefreshedAt.Equal(before.LastRefreshedAt) {
					t.Fatal("control-only update lost the existing refresh lifecycle")
				}
				if got.Index != before.Index || got.RegistrationEpoch != before.RegistrationEpoch || got.Metadata["codex_identity_namespace"] != "stable-namespace" || got.ProxyURL != incoming.ProxyURL {
					t.Fatal("credential replacement lost identity or routing")
				}
				if !tc.disabled && (got.Quota.Reason != "credential_quota" || !got.Quota.Exceeded || !got.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt) || got.ModelStates["old-model"] == nil || got.ModelStates["old-model"].LastError.HTTPStatus != 503) {
					t.Fatal("credential replacement lost quota or model failure")
				}
				if tc.disabled && (!got.Disabled || got.Status != coreauth.StatusDisabled) {
					t.Fatal("replacement reenabled disabled credential")
				}
				executor := &reauthRefreshExecutorV828{}
				m.RegisterExecutor(executor)
				m.StartAutoRefresh(ctx, 5*time.Second)
				defer m.StopAutoRefresh()
				synctest.Wait()
				want := int64(0)
				if replace {
					want = 1
				}
				if calls := executor.calls.Load(); calls != want {
					t.Fatalf("automatic refresh calls=%d,want%d", calls, want)
				}
				after, _ := m.GetByID(before.ID)
				if tc.siwc && (after.Metadata["access_token"] != before.Metadata["access_token"] || after.Metadata["refresh_token"] != before.Metadata["refresh_token"]) {
					t.Fatal("SIWC-only refresh changed the outer credentials")
				}
				if !tc.disabled && (!after.Quota.Exceeded || after.Quota.Reason != "credential_quota" || after.ModelStates["old-model"] == nil || after.ModelStates["old-model"].LastError.HTTPStatus != 503) {
					t.Fatal("automatic refresh cleared quota or model failure")
				}

				if tc.disabled && (!after.Disabled || after.Status != coreauth.StatusDisabled) {
					t.Fatal("automatic refresh reenabled disabled credential")
				}
				if n, limit := m.CredentialConcurrencySnapshot(before.ID); n != 1 || limit != 3 {
					t.Fatalf("held lease changed: %d/%d", n, limit)
				}
				release()
				if n, _ := m.CredentialConcurrencySnapshot(before.ID); n != 0 {
					t.Fatal("old lease failed to release")
				}
			})
		})
	}
}
