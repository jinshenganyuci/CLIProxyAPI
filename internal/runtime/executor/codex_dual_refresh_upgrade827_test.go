package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCodexDualRefreshPreservesProxyIdentityAndLiveLeaseUpgrade827(t *testing.T) {
	for _, failSIWC := range []bool{false, true} {
		for _, explicitLimit := range []int{0, 3} {
			t.Run(fmt.Sprintf("partial=%t/limit=%d", failSIWC, explicitLimit), func(t *testing.T) {
				proxyURL, outerCount, siwcCount, teardown := startDualCodexAndSIWCMockOAuthServer(t, "", &failSIWC)
				defer teardown()
				var wrongRoute atomic.Int32
				canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					wrongRoute.Add(1)
					w.WriteHeader(http.StatusBadGateway)
				}))
				defer canary.Close()
				directory := t.TempDir()
				store := fileauth.NewFileTokenStore()
				store.SetBaseDir(directory)
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: canary.URL}}
				cfg.Codex.CredentialProxyPolicy = config.CodexCredentialProxyPolicyRequire
				manager := coreauth.NewManager(store, nil, nil)
				manager.RegisterExecutor(NewCodexExecutor(cfg))
				const namespace = "af1c5a51-281b-46e5-9db7-2e2d27700e9e"
				siwc := map[string]any{"access_token": "siwc-old", "refresh_token": "siwc-refresh-old", "id_token": "siwc-id-old", "client_id": "synthetic-client", "device_id": "synthetic-device", "expired": "2000-01-01T00:00:00Z"}
				metadata := map[string]any{"type": "codex", "id_token": "outer-id-old", "access_token": "outer-old", "refresh_token": "outer-refresh-old", "account_id": "account-old", "email": "old@example.invalid", "expired": "2000-01-01T00:00:00Z", "plan_type": "pro", "proxy_url": proxyURL, "codex_identity_namespace": namespace, "codex_identity_version": 1, "priority": 7, "use_siwc": true, "siwc": siwc}
				limit := explicitLimit
				if explicitLimit == 0 {
					limit = 10
				} else {
					metadata["concurrency_limit"] = explicitLimit
				}
				useSIWC := true
				storage := &codexauth.CodexTokenStorage{IDToken: "outer-id-old", AccessToken: "outer-old", RefreshToken: "outer-refresh-old", AccountID: "account-old", Email: "old@example.invalid", PlanType: "pro", IdentityVersion: 1, IdentityNamespace: namespace, UseSIWC: &useSIWC, SIWC: cloneSIWCMap(siwc)}
				auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "dual.json", FileName: "dual.json", Provider: "codex", ProxyURL: proxyURL, Storage: storage, Metadata: metadata, Attributes: map[string]string{coreauth.AttributePath: filepath.Join(directory, "dual.json"), "priority": "7"}})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), auth)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				defer release()
				ctx := core.WithRequestProxyURL(context.Background(), canary.URL)
				refreshed, errRefresh := manager.ForceRefreshAuth(ctx, auth.ID)
				if errRefresh != nil {
					t.Fatal(errRefresh)
				}
				if *outerCount != 1 || *siwcCount != 1 || wrongRoute.Load() != 0 {
					t.Fatalf("refresh escaped selected route: outer=%d inner=%d alternate=%d", *outerCount, *siwcCount, wrongRoute.Load())
				}
				if active, gotLimit := manager.CredentialConcurrencySnapshot(auth.ID); active != 1 || gotLimit != limit {
					t.Fatalf("refresh detached lease or limit: %d/%d", active, gotLimit)
				}
				raw, errRead := os.ReadFile(filepath.Join(directory, auth.FileName))
				if errRead != nil {
					t.Fatal(errRead)
				}
				var saved map[string]any
				if errDecode := json.Unmarshal(raw, &saved); errDecode != nil {
					t.Fatal(errDecode)
				}
				for _, state := range []map[string]any{refreshed.Metadata, saved} {
					for key, want := range map[string]any{"access_token": "outer-access-refreshed", "id_token": "outer-id-old", "account_id": "account-old", "email": "old@example.invalid", "proxy_url": proxyURL, "codex_identity_namespace": namespace, "use_siwc": true} {
						if state[key] != want {
							t.Fatalf("refresh lost %s", key)
						}
					}
					inner := state["siwc"].(map[string]any)
					wantAccess := "siwc-access-refreshed"
					if failSIWC {
						wantAccess = "siwc-old"
					}
					if inner["access_token"] != wantAccess || inner["device_id"] != "synthetic-device" || inner["client_id"] != "synthetic-client" {
						t.Fatalf("nested refresh lost token or device binding: %+v", inner)
					}
				}
				if storage.AccessToken != "outer-old" || storage.SIWC["access_token"] != "siwc-old" {
					t.Fatal("refresh mutated original storage snapshot")
				}
				release()
				if active, gotLimit := manager.CredentialConcurrencySnapshot(auth.ID); active != 0 || gotLimit != limit {
					t.Fatalf("lease not released: %d/%d", active, gotLimit)
				}
			})
		}
	}
}
