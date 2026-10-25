package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV842FileSnapshotTokenReplacementKeepsCooldownButNotOldRefreshGate(t *testing.T) {
	for _, change := range []string{"controls", "outer-token", "siwc-token"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "snapshot.json")
			now := time.Now()
			metadata := map[string]any{"synthetic_preserved": map[string]any{"large_integer": json.Number("9007199254740993")}, "type": "codex", "access_token": "outer-old", "refresh_token": "outer-refresh", "expired": now.Add(7 * 24 * time.Hour).Format(time.RFC3339), "proxy_url": "direct", "concurrency_limit": 3, "codex_identity_namespace": "873ba54e-5ad5-5c28-b52a-20c943f8cb5c", "use_siwc": true, "siwc": map[string]any{"access_token": "siwc-old", "refresh_token": "siwc-refresh", "client_id": "client", "expired": now.Add(time.Hour).Format(time.RFC3339)}}
			read := func() *coreauth.Auth {
				t.Helper()
				raw, err := json.Marshal(metadata)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				auths, err := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{Config: &config.Config{}, AuthDir: dir, Now: now, IDGenerator: synthesizer.NewStableIDGenerator()}, path, raw)
				if err != nil || len(auths) != 1 || !auths[0].HasFileSnapshot() {
					t.Fatalf("snapshot: %v %v", auths, err)
				}
				return auths[0]
			}
			ctx := coreauth.WithSkipPersist(context.Background())
			m := coreauth.NewManager(nil, nil, nil)
			before, err := m.Register(ctx, read())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(before.ID) })
			before.LastRefreshedAt = now.Add(-time.Hour)
			before.NextRefreshAfter = now.Add(5 * time.Minute)
			before.RefreshFailures = 2
			before.Status = coreauth.StatusError
			before.Unavailable = true
			before.NextRetryAfter = now.Add(time.Hour)
			before.Quota = coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: before.NextRetryAfter}
			before.ModelStates = map[string]*coreauth.ModelState{"old-model": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour), LastError: &coreauth.Error{HTTPStatus: 503}}}
			before, err = m.Update(ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			release, err := m.AcquireCredentialConcurrency(ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			metadata["concurrency_limit"] = 2
			metadata["proxy_url"] = "socks5://selected.invalid:1080"
			metadata["label"] = "operator-edit"
			switch change {
			case "outer-token":
				metadata["access_token"] = "outer-new"
			case "siwc-token":
				metadata["siwc"].(map[string]any)["access_token"] = "siwc-new"
			}
			snapshot := read()
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{cfg: &config.Config{AuthDir: dir}, coreManager: m}
			service.prepareCoreAuthForModelRegistration(ctx, snapshot)
			got, _ := m.GetByID(before.ID)
			if got.Generation <= before.Generation {
				t.Fatal("fresh snapshot was not applied")
			}
			if change == "controls" {
				if !got.NextRefreshAfter.Equal(before.NextRefreshAfter) || !got.LastRefreshedAt.Equal(before.LastRefreshedAt) || got.RefreshFailures != before.RefreshFailures {
					t.Fatal("control snapshot erased active refresh gate")
				}
			} else if !got.NextRefreshAfter.IsZero() || !got.LastRefreshedAt.IsZero() || got.RefreshFailures != 0 {
				t.Fatalf("new token inherited obsolete refresh gate: next=%v last=%v failures=%d", got.NextRefreshAfter, got.LastRefreshedAt, got.RefreshFailures)
			}
			if !got.Unavailable || !got.Quota.Exceeded || !got.NextRetryAfter.Equal(before.NextRetryAfter) || got.ModelStates["old-model"] == nil {
				t.Fatal("file snapshot erased cooldown")
			}
			if got.RegistrationEpoch != before.RegistrationEpoch || got.ProxyURL != metadata["proxy_url"] || got.Metadata["codex_identity_namespace"] != metadata["codex_identity_namespace"] {
				t.Fatal("snapshot changed identity or route")
			}
			diskAfter, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(diskBefore, diskAfter) || !bytes.Contains(diskAfter, []byte("9007199254740993")) {
				t.Fatal("snapshot application changed the exact numeric disk metadata")
			}
			if n, limit := m.CredentialConcurrencySnapshot(before.ID); n != 1 || limit != 2 {
				t.Fatalf("snapshot lost lease: %d/%d", n, limit)
			}
			release()
			if n, _ := m.CredentialConcurrencySnapshot(before.ID); n != 0 {
				t.Fatal("old lease did not release")
			}
		})
	}
}
