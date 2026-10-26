package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestV844SIWCToggleRetainsRefreshStateWithoutBlockingNewTokens(t *testing.T) {
	for _, path := range []string{"update", "modify", "metadata", "snapshot"} {
		for _, change := range []string{"toggle", "quota-toggle", "controls", "outer-token", "siwc-token"} {
			t.Run(path+"/"+change, func(t *testing.T) {
				m, _, base := newMetadataControlsFixture(t, nil)
				base.Metadata["use_siwc"] = true
				base.Metadata["expired"] = "2100-01-01T00:00:00Z"
				base.Metadata["codex_identity_namespace"] = "stable-toggle-namespace"
				base.Metadata["siwc"] = map[string]any{"access_token": "rejected-siwc", "refresh_token": "same-refresh", "client_id": "same-client", "expired": "2100-01-01T00:00:00Z"}
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
				gate := now.Add(5 * time.Minute)
				refreshedAt := now.Add(-time.Hour)
				m.mu.Lock()
				current := m.auths[base.ID]
				current.RejectedAccessToken, current.RejectedSIWCAccessToken = "rejected-siwc", "rejected-siwc"
				current.NextRefreshAfter, current.LastRefreshedAt, current.RefreshFailures = gate, refreshedAt, 3
				current.Unavailable, current.Status = true, StatusError
				current.LastError = &Error{HTTPStatus: 401, Message: "unauthorized"}
				if change == "quota-toggle" {
					current.NextRetryAfter = now.Add(time.Hour)
					current.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: current.NextRetryAfter}
				}
				base = current.Clone()
				m.mu.Unlock()
				apply := func(a *Auth) error {
					a.ProxyURL = "direct"
					a.Metadata["proxy_url"] = "direct"
					a.Metadata["concurrency_limit"] = 3
					if change != "controls" && change != "siwc-token" {
						a.Metadata["use_siwc"] = false
					}
					if change == "outer-token" {
						a.Metadata["access_token"] = "fresh-outer"
					}
					if change == "siwc-token" {
						a.Metadata["siwc"].(map[string]any)["access_token"] = "fresh-siwc"
					}
					return nil
				}
				var got *Auth
				switch path {
				case "update":
					replacement := base.Clone()
					_ = apply(replacement)
					got, err = m.Update(t.Context(), replacement)
				case "modify":
					var found bool
					got, found, err = m.Modify(t.Context(), base.ID, base.RegistrationEpoch, apply)
					if !found {
						t.Fatal("auth missing during modify")
					}
				case "metadata":
					got, err = m.UpdateAndPersistMetadata(t.Context(), base.ID, apply)
				case "snapshot":
					snapshot := base.Clone()
					_ = apply(snapshot)
					snapshot.Generation, snapshot.RegistrationEpoch = 0, 0
					snapshot.LastRefreshedAt, snapshot.NextRefreshAfter, snapshot.RefreshFailures = time.Time{}, time.Time{}, 0
					file := filepath.Join(t.TempDir(), "snapshot.json")
					data, errJSON := json.Marshal(snapshot.Metadata)
					if errJSON != nil {
						t.Fatal(errJSON)
					}
					if errWrite := os.WriteFile(file, data, 0600); errWrite != nil {
						t.Fatal(errWrite)
					}
					snapshot.BindFileSnapshot(file, data)
					got, err = m.Update(WithSkipPersist(t.Context()), snapshot)
				}
				if err != nil {
					t.Fatal(err)
				}
				newTokens := change == "outer-token" || change == "siwc-token"
				if newTokens {
					if !got.NextRefreshAfter.IsZero() || got.RefreshFailures != 0 {
						t.Error("new token inherited obsolete refresh gate or failures")
					}
				} else {
					if !got.NextRefreshAfter.Equal(gate) || got.RefreshFailures != 3 || !got.LastRefreshedAt.Equal(refreshedAt) {
						t.Errorf("non-token edit reset refresh state: gate=%v failures=%d refreshed=%v", got.NextRefreshAfter, got.RefreshFailures, got.LastRefreshedAt)
					}
					if change == "toggle" || change == "quota-toggle" {
						if next, ok := nextRefreshCheckAt(now, got, time.Second); !ok || !next.Equal(gate) {
							t.Errorf("unchanged rejected SIWC was immediately rescheduled: next=%v ok=%v", next, ok)
						}
					}
				}
				if change == "quota-toggle" {
					if !got.Unavailable || !got.Quota.Exceeded || !got.NextRetryAfter.Equal(base.NextRetryAfter) {
						t.Error("toggle released active quota")
					}
				} else if change != "controls" && (got.Unavailable || !got.HasValidAccessToken(now)) {
					t.Error("current valid token remained unavailable")
				}
				if change != "siwc-token" && got.RejectedSIWCAccessToken != "rejected-siwc" {
					t.Error("unchanged nested rejection was cleared")
				}
				if change == "siwc-token" && got.RejectedSIWCAccessToken != "" {
					t.Error("new SIWC kept obsolete rejection")
				}
				if got.RegistrationEpoch != base.RegistrationEpoch || got.ProxyURL != "direct" || got.Metadata["codex_identity_namespace"] != "stable-toggle-namespace" {
					t.Fatal("identity or accepted route changed")
				}
				if change != "controls" && got.CredentialVersion <= base.CredentialVersion {
					t.Error("routing change lost credential-version fence")
				}
				if change == "toggle" || change == "quota-toggle" {
					reenabled, found, errEnable := m.Modify(t.Context(), base.ID, base.RegistrationEpoch, func(a *Auth) error {
						a.Metadata["use_siwc"] = true
						return nil
					})
					if errEnable != nil || !found {
						t.Fatalf("reenable SIWC: found=%v error=%v", found, errEnable)
					}
					if reenabled.CodexSIWCStatus(now) != SIWCStatusInvalid || reenabled.RejectedSIWCAccessToken != "rejected-siwc" || !reenabled.NextRefreshAfter.Equal(gate) || reenabled.RefreshFailures != 3 {
						t.Error("reenabling unchanged rejected SIWC erased its rejection or refresh state")
					}
				}
				requireCredentialConcurrencySnapshot(t, m, base.ID, 1, 3)
				release()
				requireCredentialConcurrencySnapshot(t, m, base.ID, 0, 3)
			})
		}
	}
}
