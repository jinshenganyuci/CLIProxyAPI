package auth

import (
	"testing"
	"time"
)

func TestV844DurableReloginRecoversRefreshFailureAndPreservesControls(t *testing.T) {
	for _, scenario := range []string{"invalid-grant", "transient-expiry", "active-quota", "controls-only", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			m, store, base := newMetadataControlsFixture(t, nil)
			release, err := m.AcquireCredentialConcurrency(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			base.Status = StatusError
			base.Unavailable = true
			base.RefreshFailures = 4
			base.NextRefreshAfter = time.Now().Add(5 * time.Minute)
			base.LastError = &Error{HTTPStatus: 400, Message: "invalid_grant"}
			base.StatusMessage = "invalid_grant"
			if scenario == "transient-expiry" {
				base.LastError = &Error{HTTPStatus: 503, Message: "token endpoint unavailable"}
				base.StatusMessage = "token expired"
			}
			if scenario == "active-quota" {
				base.NextRetryAfter = time.Now().Add(time.Hour)
				base.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: base.NextRetryAfter}
			}
			if scenario == "disabled" {
				base.Disabled = true
				base.Status = StatusDisabled
			}
			base, err = m.Update(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			got, err := m.UpdateAndPersistMetadata(t.Context(), base.ID, func(a *Auth) error {
				a.ProxyURL = "direct"
				a.Metadata["proxy_url"] = "direct"
				a.Metadata["concurrency_limit"] = 3
				a.Metadata["codex_identity_namespace"] = "relogin-preserved-namespace"
				if scenario != "controls-only" {
					a.Metadata["access_token"] = "new-access"
					a.Metadata["refresh_token"] = "new-refresh"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "controls-only":
				if !got.Unavailable || !got.NextRefreshAfter.Equal(base.NextRefreshAfter) || got.RefreshFailures != base.RefreshFailures {
					t.Fatal("control-only update erased refresh failure")
				}
			case "disabled":
				if !got.Disabled || got.Status != StatusDisabled || got.Unavailable != base.Unavailable || !got.NextRefreshAfter.Equal(base.NextRefreshAfter) || got.RefreshFailures != base.RefreshFailures {
					t.Fatal("disabled update changed its existing availability or refresh state")
				}
			default:
				if got.RefreshFailures != 0 || !got.NextRefreshAfter.IsZero() {
					t.Errorf("relogin retained obsolete refresh state: failures=%d gate=%v", got.RefreshFailures, got.NextRefreshAfter)
				}
				if scenario == "active-quota" {
					if !got.Unavailable || !got.Quota.Exceeded || !got.NextRetryAfter.Equal(base.NextRetryAfter) {
						t.Error("relogin released a live quota")
					}
					if got.LastError != nil || got.StatusMessage != got.Quota.Reason {
						t.Error("live quota retained the obsolete grant failure")
					}
				} else if got.Unavailable || got.Status != StatusActive || got.LastError != nil || !got.NextRetryAfter.IsZero() {
					t.Error("durably re-logged credential is still unavailable")
				}
			}
			if got.RegistrationEpoch != base.RegistrationEpoch || got.ProxyURL != "direct" || got.Metadata["codex_identity_namespace"] != "relogin-preserved-namespace" {
				t.Fatal("relogin changed identity or route")
			}
			disk, err := store.List(t.Context())
			if err != nil || len(disk) != 1 {
				t.Fatalf("durable state %v %v", disk, err)
			}
			if disk[0].Metadata["codex_identity_namespace"] != got.Metadata["codex_identity_namespace"] || disk[0].Metadata["access_token"] != got.Metadata["access_token"] {
				t.Fatal("runtime did not match the accepted durable credential")
			}
			requireCredentialConcurrencySnapshot(t, m, base.ID, 1, 3)
			release()
			requireCredentialConcurrencySnapshot(t, m, base.ID, 0, 3)
		})
	}
}
