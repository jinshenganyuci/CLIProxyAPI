package management

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexPlanAcquisitionPreservesForkControlsUpgrade804(t *testing.T) {
	for _, testCase := range []struct{ name, claimPlan, tokenPlan, want string }{
		{"claim_over_token", "pro", "team", "pro"},
		{"token_without_claim", "", "team", "team"},
		{"default_free", "", "", "free"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newCodexOAuthProxyTestHandler(t, "http://global.invalid:8080", "require")
			target := registerCodexOAuthTarget(t, h)
			h.postAuthPersistHook = func(ctx context.Context, auth *coreauth.Auth) error {
				_, errUpdate := h.authManager.Update(ctx, auth)
				return errUpdate
			}
			lease, errAcquire := h.authManager.AcquireCredentialConcurrency(context.Background(), target)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer lease()
			options, status, errPrepare := prepareCodexOAuthProxyTest(t, h, "POST", "/v8/management/oauth/auth-url?provider=codex", `{"auth_index":"`+lockedAuthIndex(target)+`"}`)
			if status != 200 || errPrepare != nil {
				t.Fatal("could not select existing credential before acquisition")
			}
			bundle := &codex.CodexAuthBundle{TokenData: codex.CodexTokenData{
				IDToken: makeOAuthTestJWT(testCase.claimPlan), AccountID: "selected-account", Email: "same@example.test",
				AccessToken: "new-access", RefreshToken: "new-refresh", PlanType: testCase.tokenPlan,
			}}
			record, errRecord := options.record(&fakeCodexOAuthService{}, bundle)
			if errRecord != nil {
				t.Fatal(errRecord)
			}
			if record.Metadata["plan_type"] != testCase.want || record.Attributes["plan_type"] != testCase.want || record.Storage.(*codex.CodexTokenStorage).PlanType != testCase.want {
				t.Fatal("plan was not captured in all acquisition representations")
			}
			state := codex.NewCredentialIdentityNamespace()
			RegisterOAuthSession(state, "codex")
			t.Cleanup(func() { CancelOAuthSession(state) })
			if _, errSave := options.save(context.Background(), state, record); errSave != nil {
				t.Fatal(errSave)
			}
			persisted := readCodexIdentityTestFile(t, filepath.Join(h.cfg.AuthDir, target.FileName))
			if persisted["plan_type"] != testCase.want || persisted["proxy_url"] != target.ProxyURL || !codexIdentityFieldsEqual(persisted, target.Metadata) || persisted["access_token"] != "new-access" {
				t.Fatal("plan persistence changed token, selected proxy or namespace")
			}
			if current, exists := h.authManager.GetByID(target.ID); !exists || current.Attributes["plan_type"] != testCase.want || current.RegistrationEpoch != target.RegistrationEpoch {
				t.Fatal("reauthentication changed active credential binding")
			}
			if inflight, limit := h.authManager.CredentialConcurrencySnapshot(target.ID); inflight != 1 || limit != 10 {
				t.Fatalf("plan persistence changed active admission: %d/%d", inflight, limit)
			}
		})
	}
}

func TestCodexPlanFieldSyncFallbackKeepsOtherControlsUpgrade804(t *testing.T) {
	for _, testCase := range []struct{ name, explicit, token, want string }{
		{"explicit_priority", "team", makeOAuthTestJWT("pro"), "team"},
		{"claim", "", makeOAuthTestJWT("pro"), "pro"},
		{"empty_claim", "", makeOAuthTestJWT(""), "free"},
		{"invalid_claim", "", "invalid.synthetic.jwt", "free"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			auth := &coreauth.Auth{Provider: "codex", ProxyURL: "direct", Metadata: map[string]any{"id_token": testCase.token, "plan_type": testCase.explicit, "concurrency_limit": 3}, Attributes: map[string]string{"plan_type": "old", "priority": "7"}}
			syncAuthFilePlanTypeAttribute(auth)
			if auth.Attributes["plan_type"] != testCase.want || auth.ProxyURL != "direct" || auth.Attributes["priority"] != "7" || coreauth.EffectiveConcurrencyLimit(auth) != 3 {
				t.Fatal("plan field synchronization changed unrelated controls")
			}
		})
	}
}
