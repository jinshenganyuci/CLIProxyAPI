package management

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV836PendingSIWCAccountFencePreservesForkState(t *testing.T) {
	for _, replaceAccount := range []bool{false, true} {
		name := "same-account-token-update"
		if replaceAccount {
			name = "different-account-same-epoch"
		}
		t.Run(name, func(t *testing.T) {
			f := setupSIWCFlow(t, true)
			const namespace = "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7"
			raw := []byte(`{"type":"codex","access_token":"outer","refresh_token":"outer-refresh","account_id":"account","proxy_url":"direct","concurrency_limit":3,"codex_identity_version":1,"codex_identity_namespace":"981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7","opaque":{"large":9007199254740993,"decimal":1.0000000000000001}}`)
			if errWrite := os.WriteFile(f.path, raw, 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if errDecode := json.Unmarshal(raw, &f.auth.Metadata); errDecode != nil {
				t.Fatal(errDecode)
			}
			f.auth.ProxyURL = "direct"
			f.h.cfg.Codex.CredentialProxyPolicy = "require"
			f.h.cfg.ProxyURL = "http://global-canary.invalid:8080"
			f.h.siwcOAuthFactory = func(cfg *config.Config) siwcOAuthService {
				if cfg.ProxyURL != "direct" {
					t.Error("SIWC exchange did not retain selected direct route")
				}
				return f.service
			}
			current, errUpdate := f.manager.Update(context.Background(), f.auth)
			if errUpdate != nil {
				t.Fatal(errUpdate)
			}
			release, errLease := f.manager.AcquireCredentialConcurrency(context.Background(), current)
			if errLease != nil {
				t.Fatal(errLease)
			}
			t.Cleanup(release)
			state, flow := f.start(t)
			response := siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"provider": "codex", "redirect_url": siwcCallbackURL(state)})
			if response.Code != http.StatusOK {
				t.Fatal("SIWC callback was not accepted")
			}
			<-f.service.inputs
			replacement := current.Clone()
			replacement.Metadata["access_token"] = "outer-latest"
			replacement.Metadata["refresh_token"] = "refresh-latest"
			if replaceAccount {
				replacement.Metadata["account_id"] = "account-b"
			}
			if errWrite := codex.WriteJSONFileAtomic(f.path, replacement.Metadata); errWrite != nil {
				t.Fatal(errWrite)
			}
			updated, errUpdate := f.manager.Update(context.Background(), replacement)
			if errUpdate != nil || updated.RegistrationEpoch != current.RegistrationEpoch {
				t.Fatalf("replacement did not keep the registration epoch: %v", errUpdate)
			}
			f.unblock()
			waitSIWCExchange(t, flow)
			wantStatus := "ok"
			if replaceAccount {
				wantStatus = "error"
			}
			if status := performOAuthStatusRequest(t, f.router, state); status.Status != wantStatus {
				t.Fatalf("binding status=%q, want %q", status.Status, wantStatus)
			}
			final, _ := f.manager.GetByID(current.ID)
			if final == nil || final.Metadata["access_token"] != "outer-latest" || final.Metadata["refresh_token"] != "refresh-latest" || final.Metadata["codex_identity_namespace"] != namespace || final.ProxyURL != "direct" || coreauth.EffectiveConcurrencyLimit(final) != 3 {
				t.Fatal("binding changed the latest outer credential or fork controls")
			}
			if (final.Metadata["use_siwc"] == true) == replaceAccount {
				t.Fatal("SIWC publication did not follow the captured account")
			}
			disk, errRead := os.ReadFile(f.path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			var saved struct {
				Opaque map[string]json.RawMessage `json:"opaque"`
			}
			if errDecode := json.Unmarshal(disk, &saved); errDecode != nil || string(saved.Opaque["large"]) != "9007199254740993" || string(saved.Opaque["decimal"]) != "1.0000000000000001" {
				t.Fatal("existing precise metadata changed during SIWC commit")
			}
			if count, limit := f.manager.CredentialConcurrencySnapshot(current.ID); count != 1 || limit != 3 {
				t.Fatalf("pending SIWC changed held lease: %d/%d", count, limit)
			}
			release()
			if count, _ := f.manager.CredentialConcurrencySnapshot(current.ID); count != 0 {
				t.Fatal("pre-bind lease could not be released")
			}
		})
	}
}
