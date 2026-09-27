package pluginhost

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func registerQuotaAuthFixture(t *testing.T, host *Host, auth *coreauth.Auth) *coreauth.Auth {
	t.Helper()
	if host.currentAuthManager() == nil {
		host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	}
	auth.EnsureIndex()
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register quota credential: %v", errRegister)
	}
	return auth
}

type nativeQuotaClientTrap struct{ calls atomic.Int64 }

func (c *nativeQuotaClientTrap) Do(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.calls.Add(1)
	return pluginapi.HTTPResponse{}, fmt.Errorf("caller-supplied client must not execute")
}

func (c *nativeQuotaClientTrap) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	c.calls.Add(1)
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("caller-supplied stream client must not execute")
}

func nativeQuotaRouteServer(t *testing.T, route string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	count := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		_, _ = io.WriteString(w, route)
	}))
	t.Cleanup(server.Close)
	return server, count
}

func nativeQuotaHTTPHost(endpoint string, calls *atomic.Int64, before func(), profile *pluginapi.HTTPWireProfile) *Host {
	do := func(ctx context.Context, client pluginapi.HostHTTPClient) (string, error) {
		calls.Add(1)
		if before != nil {
			before()
		}
		response, errDo := client.Do(ctx, pluginapi.HTTPRequest{URL: endpoint, WireProfile: profile})
		return string(response.Body), errDo
	}
	provider := &testQuotaProvider{
		identifier: "native-quota-test",
		fetchFn: func(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			body, errDo := do(ctx, req.HTTPClient)
			return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: body}}, errDo
		},
		resetFn: func(ctx context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
			body, errDo := do(ctx, req.HTTPClient)
			return pluginapi.QuotaResetResponse{Success: errDo == nil, Message: body}, errDo
		},
	}
	return newHostWithRecords(capabilityRecord{id: "native-quota-test", plugin: pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{QuotaProvider: provider},
	}})
}

func invokeNativeQuota(host *Host, operation, index, id string, client pluginapi.HostHTTPClient) (string, bool, error) {
	// A plugin-selected provider label must not replace the credential's policy.
	if operation == "fetch" {
		response, handled, errFetch := host.FetchQuotaByPlugin(context.Background(), "native-quota-test", pluginapi.QuotaFetchRequest{
			Provider: "plugin-provider-override", AuthIndex: index, AuthID: id, HTTPClient: client,
		})
		if response.Subscription != nil {
			return response.Subscription.Plan, handled, errFetch
		}
		return "", handled, errFetch
	}
	response, handled, errReset := host.ResetQuotaByPlugin(context.Background(), "native-quota-test", pluginapi.QuotaResetRequest{
		Provider: "plugin-provider-override", AuthIndex: index, AuthID: id, HTTPClient: client,
	})
	return response.Message, handled, errReset
}

func TestNativeQuotaCredentialProxyWithoutPhysicalJSON(t *testing.T) {
	for _, storage := range []string{"no-path", "missing-file", "invalid-json", "empty-file"} {
		for _, operation := range []string{"fetch", "reset"} {
			for _, wire := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/wire-profile=%v", storage, operation, wire), func(t *testing.T) {
					direct, directCalls := nativeQuotaRouteServer(t, "direct")
					selected, selectedCalls := nativeQuotaRouteServer(t, "selected")
					global, globalCalls := nativeQuotaRouteServer(t, "global")
					var providerCalls atomic.Int64
					var profile *pluginapi.HTTPWireProfile
					if wire {
						profile = &pluginapi.HTTPWireProfile{HTTP1Only: true}
					}
					host := nativeQuotaHTTPHost(direct.URL, &providerCalls, nil, profile)
					host.runtimeConfig = &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: "require"}}
					host.runtimeConfig.ProxyURL = global.URL
					auth := &coreauth.Auth{ID: "codex-proxied", Provider: "codex", ProxyURL: selected.URL,
						Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth}}
					if storage != "no-path" {
						path := filepath.Join(t.TempDir(), "auth.json")
						auth.Attributes["path"] = path
						if storage != "missing-file" {
							payload := []byte("not-json")
							if storage == "empty-file" {
								payload = nil
							}
							if errWrite := os.WriteFile(path, payload, 0o600); errWrite != nil {
								t.Fatal(errWrite)
							}
						}
					}
					registerQuotaAuthFixture(t, host, auth)
					callerClient := &nativeQuotaClientTrap{}
					var suppliedClient pluginapi.HostHTTPClient
					if wire {
						suppliedClient = callerClient
					}
					body, handled, errCall := invokeNativeQuota(host, operation, auth.Index, "", suppliedClient)
					if errCall != nil || !handled || body != "selected" {
						t.Fatalf("quota = %q, %v, %v; want selected route", body, handled, errCall)
					}
					if selectedCalls.Load() != 1 || globalCalls.Load() != 0 || directCalls.Load() != 0 || callerClient.calls.Load() != 0 || providerCalls.Load() != 1 {
						t.Fatalf("calls selected/global/direct/caller/provider = %d/%d/%d/%d/%d", selectedCalls.Load(), globalCalls.Load(), directCalls.Load(), callerClient.calls.Load(), providerCalls.Load())
					}
				})
			}
		}
	}
}

func TestNativeQuotaCredentialProxyRejectsBeforeProvider(t *testing.T) {
	for _, operation := range []string{"fetch", "reset"} {
		for _, scenario := range []string{"invalid-selected", "missing-required", "unknown-index", "unknown-id", "mismatched-selectors", "non-codex-invalid"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				server, networkCalls := nativeQuotaRouteServer(t, "unexpected")
				var providerCalls atomic.Int64
				host := nativeQuotaHTTPHost(server.URL, &providerCalls, nil, nil)
				host.runtimeConfig = &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: "require"}}
				host.runtimeConfig.ProxyURL = server.URL
				auth := &coreauth.Auth{ID: "selected-auth", Provider: "codex", ProxyURL: server.URL}
				if scenario == "invalid-selected" || scenario == "non-codex-invalid" {
					auth.ProxyURL = "invalid-proxy-setting"
				}
				if scenario == "non-codex-invalid" {
					auth.Provider = "other"
				}
				if scenario == "missing-required" {
					auth.ProxyURL = ""
				}
				registerQuotaAuthFixture(t, host, auth)
				index, id := auth.Index, ""
				switch scenario {
				case "unknown-index":
					index = "unknown-auth-index"
				case "unknown-id":
					index, id = "", "unknown-auth-id"
				case "mismatched-selectors":
					other := registerQuotaAuthFixture(t, host, &coreauth.Auth{ID: "other-auth", Provider: "codex", ProxyURL: "direct"})
					id = other.ID
				}
				callerClient := &nativeQuotaClientTrap{}
				_, handled, errCall := invokeNativeQuota(host, operation, index, id, callerClient)
				if errCall == nil || !handled {
					t.Fatalf("quota handled/error = %v/%v; want handled rejection", handled, errCall)
				}
				if networkCalls.Load() != 0 || providerCalls.Load() != 0 || callerClient.calls.Load() != 0 {
					t.Fatalf("rejected request escaped: network/provider/caller = %d/%d/%d", networkCalls.Load(), providerCalls.Load(), callerClient.calls.Load())
				}
			})
		}
	}
}

func TestNativeQuotaCredentialProxyConcurrentAuthIDSelection(t *testing.T) {
	direct, directCalls := nativeQuotaRouteServer(t, "direct")
	global, globalCalls := nativeQuotaRouteServer(t, "global")
	var providerCalls atomic.Int64
	host := nativeQuotaHTTPHost(direct.URL, &providerCalls, nil, nil)
	host.runtimeConfig = &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: "require"}}
	host.runtimeConfig.ProxyURL = global.URL
	var wg sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan error, 4)
	for _, id := range []string{"credential-a", "credential-b"} {
		proxy, proxyCalls := nativeQuotaRouteServer(t, id)
		registerQuotaAuthFixture(t, host, &coreauth.Auth{ID: id, Provider: "codex", ProxyURL: proxy.URL})
		t.Cleanup(func() {
			if proxyCalls.Load() != 2 {
				t.Errorf("%s requests = %d, want 2", id, proxyCalls.Load())
			}
		})
		for _, operation := range []string{"fetch", "reset"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				body, handled, errCall := invokeNativeQuota(host, operation, "", id, nil)
				if errCall != nil || !handled || body != id {
					errors <- fmt.Errorf("%s %s quota = %q/%v/%v", id, operation, body, handled, errCall)
				}
			}()
		}
	}
	close(start)
	wg.Wait()
	close(errors)
	for errCall := range errors {
		t.Error(errCall)
	}
	if directCalls.Load() != 0 || globalCalls.Load() != 0 || providerCalls.Load() != 4 {
		t.Fatalf("concurrent requests direct/global/provider = %d/%d/%d", directCalls.Load(), globalCalls.Load(), providerCalls.Load())
	}
}

func TestNativeQuotaCredentialProxyFreezesInheritedRoute(t *testing.T) {
	for _, operation := range []string{"fetch", "reset"} {
		t.Run(operation, func(t *testing.T) {
			selected, selectedCalls := nativeQuotaRouteServer(t, "selected-at-start")
			changed, changedCalls := nativeQuotaRouteServer(t, "changed-global")
			var providerCalls atomic.Int64
			var host *Host
			host = nativeQuotaHTTPHost(changed.URL, &providerCalls, func() {
				host.mu.Lock()
				host.runtimeConfig = &config.Config{}
				host.runtimeConfig.ProxyURL = changed.URL
				host.mu.Unlock()
			}, nil)
			host.runtimeConfig = &config.Config{}
			host.runtimeConfig.ProxyURL = selected.URL
			auth := registerQuotaAuthFixture(t, host, &coreauth.Auth{ID: "inherited", Provider: "codex"})
			body, handled, errCall := invokeNativeQuota(host, operation, auth.Index, "", nil)
			if errCall != nil || !handled || body != "selected-at-start" || selectedCalls.Load() != 1 || changedCalls.Load() != 0 {
				t.Fatalf("inherited route changed: body/handled/error = %q/%v/%v; selected/changed = %d/%d", body, handled, errCall, selectedCalls.Load(), changedCalls.Load())
			}
		})
	}
}

func TestNativeQuotaExplicitDirectBypassesEnvironment(t *testing.T) {
	const childEnv = "CPA_NATIVE_QUOTA_DIRECT_TEST"
	if os.Getenv(childEnv) != "1" {
		// A subprocess isolates net/http's cached environment proxy configuration.
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeQuotaExplicitDirectBypassesEnvironment$", "-test.v")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, errRun := cmd.CombinedOutput(); errRun != nil {
			t.Fatalf("direct route subprocess: %v\n%s", errRun, output)
		}
		return
	}
	global, globalCalls := nativeQuotaRouteServer(t, "global")
	environment, environmentCalls := nativeQuotaRouteServer(t, "environment")
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, environment.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	direct, directCalls := nativeQuotaRouteServer(t, "direct")
	// "localhost." resolves locally but does not trigger the standard library's
	// literal "localhost" or loopback-address proxy exemption.
	_, port, errSplit := net.SplitHostPort(strings.TrimPrefix(direct.URL, "http://"))
	if errSplit != nil {
		t.Fatal(errSplit)
	}
	endpoint := "http://localhost.:" + port
	response, errProbe := http.Get(endpoint)
	if errProbe != nil {
		t.Fatal(errProbe)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	if errClose := response.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if environmentCalls.Load() != 1 || directCalls.Load() != 0 {
		t.Fatal("environment proxy control did not use the configured proxy")
	}
	for _, operation := range []string{"fetch", "reset"} {
		for _, globalURL := range []string{"", global.URL} {
			var providerCalls atomic.Int64
			host := nativeQuotaHTTPHost(endpoint, &providerCalls, nil, nil)
			host.runtimeConfig = &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: "require"}}
			host.runtimeConfig.ProxyURL = globalURL
			auth := registerQuotaAuthFixture(t, host, &coreauth.Auth{ID: "direct-auth", Provider: "codex", ProxyURL: "direct"})
			body, handled, errCall := invokeNativeQuota(host, operation, auth.Index, "", nil)
			if errCall != nil || !handled || body != "direct" {
				t.Fatalf("direct quota = %q/%v/%v", body, handled, errCall)
			}
		}
	}
	if directCalls.Load() != 4 || globalCalls.Load() != 0 || environmentCalls.Load() != 1 {
		t.Fatalf("direct/global/environment requests = %d/%d/%d, want 4/0/1", directCalls.Load(), globalCalls.Load(), environmentCalls.Load())
	}
}
