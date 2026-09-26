package pluginhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginFilePriorityRefreshPreservesForkControlsUpgrade(t *testing.T) {
	for _, mode := range []string{"unchanged", "zero", "negative", "removed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			const id = "plugin-priority-upgrade.json"
			const namespace = "15a1a432-c2de-46a9-bcf6-6d1fda61f483"
			wantProxy, wantPriority, wantNamespace, wantLimit := "direct", "7", namespace, 10
			refreshCalls := 0
			host := newHostWithRecords(capabilityRecord{
				id: "priority-upgrade-provider",
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: fakeAuthProvider{
					identifier: "codex",
					refreshAuth: func(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
						refreshCalls++
						if req.AuthID != id || req.Attributes["priority"] != "7" {
							t.Fatal("refresh input lost file identity or priority")
						}
						if mode != "unchanged" {
							current, _ := manager.GetByID(id)
							wantProxy, wantNamespace, wantLimit = "socks5://changed.example:1080", "98c43f4b-77df-4e1a-9730-f1d487076d4e", 3
							current.ProxyURL = wantProxy
							current.Metadata["proxy_url"] = wantProxy
							current.Metadata["codex_identity_namespace"] = wantNamespace
							current.Metadata["concurrency_limit"] = wantLimit
							switch mode {
							case "zero":
								wantPriority = ""
								current.Metadata["priority"] = 0
								delete(current.Attributes, "priority")
							case "negative":
								wantPriority = "-2"
								current.Metadata["priority"] = -2
								current.Attributes["priority"] = wantPriority
							case "removed":
								wantPriority = ""
								delete(current.Metadata, "priority")
								delete(current.Attributes, "priority")
								delete(current.Attributes, coreauth.AttributeFilePriority)
							}
							if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
								t.Fatal(errUpdate)
							}
						}
						return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Metadata: map[string]any{"access_token": "new-token", "priority": 99}, Attributes: map[string]string{"priority": "99", "plugin_attr": "new-value"}}}, nil
					},
				}}},
			})
			manager.RegisterExecutor(NewPluginRefreshCompatExecutor(&stubCompatExecutor{id: "codex"}, host, &config.Config{}))
			original := &coreauth.Auth{
				ID: id, Provider: "codex", FileName: id, ProxyURL: "direct", Status: coreauth.StatusActive,
				Metadata:   map[string]any{"type": "codex", "access_token": "old-token", "priority": float64(7), "proxy_url": "direct", "codex_identity_version": float64(1), "codex_identity_namespace": namespace},
				Attributes: map[string]string{coreauth.AttributeSourceBackend: coreauth.AuthSourceFile, coreauth.AttributePath: filepath.Join(dir, id), coreauth.AttributeSource: filepath.Join(dir, id), coreauth.AttributeFilePriority: "true", "priority": "7", "custom_env": "production", "header:X-Credential": "retained"},
			}
			registered, errRegister := manager.Register(ctx, original)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(ctx, registered)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			t.Cleanup(release)
			refreshed, errRefresh := manager.ForceRefreshAuth(ctx, id)
			if errRefresh != nil {
				t.Fatal(errRefresh)
			}
			if refreshCalls != 1 || refreshed.ID != registered.ID || refreshed.Index != registered.Index || refreshed.RegistrationEpoch != registered.RegistrationEpoch {
				t.Fatal("refresh changed the binding identity or registration epoch")
			}
			if refreshed.Metadata["access_token"] != "new-token" || refreshed.ProxyURL != wantProxy || refreshed.Metadata["proxy_url"] != wantProxy || refreshed.Metadata["codex_identity_version"] != float64(1) || refreshed.Metadata["codex_identity_namespace"] != wantNamespace {
				t.Fatalf("refresh lost token, proxy, or credential identity: proxy=%q identity=%v/%v", refreshed.ProxyURL, refreshed.Metadata["codex_identity_version"], refreshed.Metadata["codex_identity_namespace"])
			}
			if refreshed.Attributes[coreauth.AttributePath] != filepath.Join(dir, id) || refreshed.Attributes[coreauth.AttributeSource] != filepath.Join(dir, id) || refreshed.Attributes[coreauth.AttributeSourceBackend] != coreauth.AuthSourceFile || refreshed.Attributes["custom_env"] != "production" || refreshed.Attributes["header:X-Credential"] != "retained" || refreshed.Attributes["plugin_attr"] != "new-value" {
				t.Fatal("partial plugin refresh lost existing file routing/custom attributes or the new plugin attribute")
			}
			if refreshed.Attributes["priority"] != wantPriority || (refreshed.Attributes[coreauth.AttributeFilePriority] == "true") != (mode != "removed") {
				t.Fatal("refresh lost the latest file priority or ownership marker")
			}
			if inflight, limit := manager.CredentialConcurrencySnapshot(id); inflight != 1 || limit != wantLimit {
				t.Fatalf("refresh changed admission: %d/%d, want1/%d", inflight, limit, wantLimit)
			}
			release()
			if inflight, _ := manager.CredentialConcurrencySnapshot(id); inflight != 0 {
				t.Fatal("refresh detached the original lease")
			}
			raw, errRead := os.ReadFile(filepath.Join(dir, id))
			if errRead != nil {
				t.Fatal(errRead)
			}
			var persisted map[string]any
			if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
				t.Fatal(errDecode)
			}
			if persisted["proxy_url"] != wantProxy || persisted["codex_identity_namespace"] != wantNamespace || persisted["access_token"] != "new-token" {
				t.Fatal("saved refresh differs from runtime controls")
			}
			if mode == "removed" {
				if _, exists := persisted["priority"]; exists {
					t.Fatal("removed priority was resurrected from plugin storage")
				}
			} else {
				priority, _ := strconv.Atoi(wantPriority)
				if persisted["priority"] != float64(priority) {
					t.Fatal("persisted priority changed")
				}
			}
		})
	}
}
