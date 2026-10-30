package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestV848DurableMetadataAdoptsRenewalStateAndRetainsControls(t *testing.T) {
	for _, change := range []string{"controls", "toggle", "new-grant", "access-only", "disabled-new-grant"} {
		t.Run(change, func(t *testing.T) {
			m, store, base := newMetadataControlsFixture(t, nil)
			base.Metadata["refresh_token"] = "rejected-refresh"
			base.Metadata["expired"] = "2100-01-01T00:00:00Z"
			base.Metadata["use_siwc"] = true
			base.Metadata["siwc"] = map[string]any{"access_token": "nested-access", "expired": "2100-01-01T00:00:00Z"}
			base.Metadata["codex_identity_namespace"] = "stable-renewal-namespace"
			base.Metadata["unknown"] = json.Number("9007199254740993")
			base.Metadata["concurrency_limit"] = 3
			var err error
			base, err = m.Update(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			release, err := m.AcquireCredentialConcurrency(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			now := time.Now()
			adopted := now.Add(-time.Hour)
			gate := now.Add(5 * time.Minute)
			m.mu.Lock()
			current := m.auths[base.ID]
			current.RequiresSignIn = true
			current.RejectedRefreshToken = "rejected-refresh"
			current.RefreshError = &Error{Code: "refresh_token_reused", Message: "refresh_token_reused", HTTPStatus: 400}
			current.RenewalEventAt, current.AdoptedAt = adopted, adopted
			current.AdoptedGrantHash = AuthGrantFingerprint(current)
			current.InferenceVerified, current.RenewalVerified = true, true
			current.LastInferenceVerifiedAt, current.LastRenewalVerifiedAt = adopted, adopted
			current.NextRefreshAfter, current.RefreshFailures = gate, 2
			if change == "disabled-new-grant" {
				current.Disabled, current.Status = true, StatusDisabled
			}
			base = current.Clone()
			m.mu.Unlock()
			updated, err := m.UpdateAndPersistMetadata(t.Context(), base.ID, func(a *Auth) error {
				a.ProxyURL = "direct"
				a.Metadata["proxy_url"] = "direct"
				if change == "toggle" {
					a.Metadata["use_siwc"] = false
				}
				if change == "new-grant" || change == "access-only" || change == "disabled-new-grant" {
					a.Metadata["access_token"] = "replacement-access"
				}
				if change == "new-grant" || change == "disabled-new-grant" {
					a.Metadata["refresh_token"] = "replacement-refresh"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if change == "controls" || change == "toggle" {
				if !updated.RequiresSignIn || updated.RejectedRefreshToken != base.RejectedRefreshToken ||
					!updated.AdoptedAt.Equal(adopted) || updated.AdoptedGrantHash != base.AdoptedGrantHash ||
					!updated.InferenceVerified || !updated.RenewalVerified || !updated.NextRefreshAfter.Equal(gate) || updated.RefreshFailures != 2 {
					t.Error("control-only edit changed renewal identity, verification or refresh gate")
				}
			} else {
				if !updated.AdoptedAt.After(adopted) || updated.AdoptedGrantHash != AuthGrantFingerprint(updated) || updated.InferenceVerified || updated.RenewalVerified {
					t.Error("new grant did not reset adoption fingerprint and verification state")
				}
				if change == "access-only" {
					if !updated.RequiresSignIn || updated.RejectedRefreshToken != "rejected-refresh" || updated.RefreshError == nil {
						t.Error("access-only replacement cleared rejected renewal grant")
					}
				} else if updated.RequiresSignIn || updated.RejectedRefreshToken != "" || updated.RefreshError != nil {
					t.Error("replacement refresh grant retained obsolete terminal renewal state")
				}
			}
			if change == "disabled-new-grant" && (!updated.Disabled || updated.Status != StatusDisabled) {
				t.Error("grant adoption enabled an explicitly disabled credential")
			}
			if updated.RegistrationEpoch != base.RegistrationEpoch || updated.ProxyURL != "direct" || updated.Metadata["codex_identity_namespace"] != "stable-renewal-namespace" || fmt.Sprint(updated.Metadata["unknown"]) != "9007199254740993" {
				t.Error("adoption changed identity, proxy or unrelated metadata")
			}
			if change != "controls" && updated.CredentialVersion <= base.CredentialVersion {
				t.Error("route or grant change did not fence old results")
			}
			persisted, errList := store.List(t.Context())
			if errList != nil || len(persisted) != 1 || persisted[0].Metadata["access_token"] != updated.Metadata["access_token"] || persisted[0].Metadata["refresh_token"] != updated.Metadata["refresh_token"] {
				t.Fatal("runtime adopted a grant different from the durable transaction")
			}
			requireCredentialConcurrencySnapshot(t, m, base.ID, 1, 3)
			release()
			requireCredentialConcurrencySnapshot(t, m, base.ID, 0, 3)
		})
	}
}

func TestV848VerifyInferenceUsesCredentialAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
	}{
		{name: "explicit-three", limit: 3},
		{name: "default-ten", limit: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			executor := &unauthorizedRefreshExecutor{id: "codex"}
			m.RegisterExecutor(executor)
			auth := &Auth{ID: t.Name(), Provider: "codex", ProxyURL: "direct", Metadata: map[string]any{"access_token": "synthetic", "codex_identity_namespace": "stable-verify"}}
			if tc.limit != 10 {
				auth.Metadata["concurrency_limit"] = tc.limit
			}
			auth, err := m.Register(t.Context(), auth)
			if err != nil {
				t.Fatal(err)
			}
			var releases []func()
			for i := 0; i < tc.limit; i++ {
				release, errAcquire := m.AcquireCredentialConcurrency(t.Context(), auth)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				defer release()
				releases = append(releases, release)
			}
			if _, errVerify := m.VerifyInference(t.Context(), auth.ID, "synthetic-model"); !IsCredentialConcurrencyError(errVerify) || len(executor.StreamCalls()) != 0 {
				t.Errorf("verification bypassed saturation: error=%v calls=%d", errVerify, len(executor.StreamCalls()))
			}
			requireCredentialConcurrencySnapshot(t, m, auth.ID, tc.limit, tc.limit)
			for _, release := range releases {
				release()
			}
			executor.onExecute = func(string) { requireCredentialConcurrencySnapshot(t, m, auth.ID, 1, tc.limit) }
			got, errVerify := m.VerifyInference(t.Context(), auth.ID, "synthetic-model")
			if errVerify != nil || !got.InferenceVerified {
				t.Fatalf("verification after release: %v", errVerify)
			}
			requireCredentialConcurrencySnapshot(t, m, auth.ID, 0, tc.limit)
		})
	}
	t.Run("conflicting-request-proxy", func(t *testing.T) {
		m := NewManager(nil, nil, nil)
		executor := &unauthorizedRefreshExecutor{id: "codex"}
		m.RegisterExecutor(executor)
		auth, err := m.Register(t.Context(), &Auth{ID: t.Name(), Provider: "codex", ProxyURL: "direct"})
		if err != nil {
			t.Fatal(err)
		}
		ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), "http://127.0.0.1:1")
		if _, errVerify := m.VerifyInference(ctx, auth.ID, "synthetic-model"); !isExecutionProxyPolicyError(errVerify) || len(executor.StreamCalls()) != 0 {
			t.Errorf("verification bypassed selected proxy: error=%v calls=%d", errVerify, len(executor.StreamCalls()))
		}
		requireCredentialConcurrencySnapshot(t, m, auth.ID, 0, 10)
	})
}

func TestV848DisabledToggleRetainsRenewalGate(t *testing.T) {
	m, _, auth := newMetadataControlsFixture(t, nil)
	auth.Metadata["use_siwc"] = true
	auth.Metadata["refresh_token"] = "rejected-refresh"
	auth.Metadata["expired"] = "2100-01-01T00:00:00Z"
	auth.Metadata["siwc"] = map[string]any{"access_token": "nested-access", "expired": "2100-01-01T00:00:00Z"}
	var err error
	auth, err = m.Update(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	gate := time.Now().Add(5 * time.Minute)
	m.mu.Lock()
	current := m.auths[auth.ID]
	current.Disabled, current.Status = true, StatusDisabled
	current.RequiresSignIn, current.RejectedRefreshToken = true, "rejected-refresh"
	current.NextRefreshAfter, current.RefreshFailures = gate, 3
	auth = current.Clone()
	m.mu.Unlock()
	updated, found, err := m.Modify(t.Context(), auth.ID, auth.RegistrationEpoch, func(a *Auth) error {
		a.Metadata["use_siwc"] = false
		return nil
	})
	if err != nil || !found {
		t.Fatalf("modify: found=%v error=%v", found, err)
	}
	if !updated.Disabled || !updated.RequiresSignIn || updated.RejectedRefreshToken != "rejected-refresh" || !updated.NextRefreshAfter.Equal(gate) || updated.RefreshFailures != 3 {
		t.Fatal("disabled route-only toggle altered terminal grant or refresh gate")
	}
	if updated.CredentialVersion <= auth.CredentialVersion || updated.AdoptedGrantHash != auth.AdoptedGrantHash {
		t.Fatal("route-only toggle lost its version fence or adopted a new grant")
	}
}
