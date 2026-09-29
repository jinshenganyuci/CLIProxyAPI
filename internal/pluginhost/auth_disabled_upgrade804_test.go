package pluginhost

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPluginDisabledSavePreservesCodexControlsAndLiveLeaseUpgrade804(t *testing.T) {
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: t.TempDir()}
	host.SetAuthManager(coreauth.NewManager(sdkauth.NewFileTokenStore(), nil, nil))
	const namespace = "178d8777-ee57-41c5-9d38-a18cbbfe20dd"
	var release func()
	for _, disabled := range []bool{false, true, false} {
		metadata := map[string]any{
			"type": "codex", "access_token": "synthetic", "refresh_token": "synthetic-refresh", "disabled": disabled,
			"proxy_url": "socks5://selected.invalid:1080", "codex_identity_version": 1, "codex_identity_namespace": namespace,
			"plan_type": "team", "concurrency_limit": 3, "priority": 7,
		}
		body, errJSON := json.Marshal(metadata)
		if errJSON != nil {
			t.Fatal(errJSON)
		}
		request, errJSON := json.Marshal(pluginapi.HostAuthSaveRequest{Name: "disabled-controls.json", JSON: body})
		if errJSON != nil {
			t.Fatal(errJSON)
		}
		raw, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, request)
		if errCall != nil {
			t.Fatal(errCall)
		}
		response, errDecode := decodeRPCEnvelope[pluginapi.HostAuthSaveResponse](raw)
		if errDecode != nil {
			t.Fatal(errDecode)
		}
		auths := host.currentAuthManager().List()
		if len(auths) != 1 {
			t.Fatal("plugin save created a second credential binding")
		}
		auth := auths[0]
		if auth.Disabled != disabled || (auth.Status == coreauth.StatusDisabled) != disabled {
			t.Fatal("plugin save did not apply native disabled state")
		}
		if auth.Metadata["codex_identity_namespace"] != namespace || auth.Metadata["proxy_url"] != metadata["proxy_url"] || auth.Metadata["plan_type"] != "team" || auth.Metadata["priority"] != float64(7) || coreauth.EffectiveConcurrencyLimit(auth) != 3 {
			t.Fatal("disabled transition lost credential controls")
		}
		if release == nil {
			var errAcquire error
			release, errAcquire = host.currentAuthManager().AcquireCredentialConcurrency(context.Background(), auth)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			t.Cleanup(release)
		}
		if inflight, limit := host.currentAuthManager().CredentialConcurrencySnapshot(auth.ID); inflight != 1 || limit != 3 {
			t.Fatalf("disabled transition detached existing lease: %d/%d", inflight, limit)
		}
		data, errRead := os.ReadFile(response.Path)
		var saved map[string]any
		if errRead != nil || json.Unmarshal(data, &saved) != nil || saved["disabled"] != disabled || saved["codex_identity_namespace"] != namespace || saved["plan_type"] != "team" || saved["proxy_url"] != metadata["proxy_url"] {
			t.Fatal("plugin disabled save persisted inconsistent fields")
		}
	}
}
