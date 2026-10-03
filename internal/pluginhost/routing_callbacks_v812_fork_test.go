package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type routingResetV812Executor struct {
	mu           sync.Mutex
	refreshCalls int
	refreshOK    bool
	routes       map[string]string
	calls        map[string]int
}

func (*routingResetV812Executor) Identifier() string { return "codex" }

func (e *routingResetV812Executor) Execute(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls[auth.ID]++
	if auth.ProxyURL != e.routes[auth.ID] {
		return coreexecutor.Response{}, errors.New("selected credential proxy changed")
	}
	if auth.Metadata["access_token"] == "synthetic-revoked" {
		return coreexecutor.Response{}, &coreauth.Error{HTTPStatus: http.StatusUnauthorized, Message: "synthetic revoked access token"}
	}
	return coreexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *routingResetV812Executor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	response, errExecute := e.Execute(ctx, auth, req, opts)
	if errExecute != nil {
		return nil, errExecute
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: response.Payload}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *routingResetV812Executor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refreshCalls++
	if coreexecutor.RequestProxyURL(ctx) != "" || auth.ProxyURL != e.routes[auth.ID] {
		return nil, errors.New("refresh escaped credential proxy policy")
	}
	if !e.refreshOK {
		return nil, errors.New("synthetic invalid_grant: refresh token revoked")
	}
	auth.Metadata["access_token"] = "synthetic-restored"
	return auth, nil
}

func (e *routingResetV812Executor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (*routingResetV812Executor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected raw HTTP request")
}

func TestRoutingResetPreservesBoundKeysTerminalAuthAndLeasesV812(t *testing.T) {
	for _, schema := range []uint32{1, pluginabi.SchemaVersion} {
		for _, limit := range []int{0, 3} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("schema=%d/limit=%d/stream=%t", schema, limit, stream), func(t *testing.T) {
					ctx := t.Context()
					ids := []string{t.Name() + "-a", t.Name() + "-b", t.Name() + "-c"}
					bindings := map[string]string{"key-a": ids[0], "key-b": ids[1], "key-c": ids[2]}
					plugin := validTestPlugin("v812-bound-scheduler")
					plugin.Capabilities.Scheduler = schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						bound := bindings[http.Header(req.Options.Headers).Get("X-Synthetic-Key")]
						for _, candidate := range req.Candidates {
							if candidate.ID == bound {
								return pluginapi.SchedulerPickResponse{Handled: true, AuthID: bound}, nil
							}
						}
						return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "auth_not_found", RejectReason: "bound credential unavailable"}, nil
					})
					lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
					lookup.schemaVersion = schema
					registeredPlugin, errPlugin := registerRPCPlugin(ctx, nil, plugin.Metadata.Name, lookup, pluginabi.MethodPluginRegister, nil)
					if errPlugin != nil {
						t.Fatal(errPlugin)
					}
					host := newHostWithRecords(capabilityRecord{id: "v812-bound-scheduler", plugin: registeredPlugin})
					store := &recordingTokenStore{}
					manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
					manager.SetPluginScheduler(host)
					host.SetAuthManager(manager)
					executor := &routingResetV812Executor{routes: map[string]string{ids[0]: "socks5://selected.invalid:1080", ids[1]: "direct", ids[2]: "http://selected.invalid:8080"}, calls: map[string]int{}}
					manager.RegisterExecutor(executor)
					const model = "v812-bound-reset-model"
					var auths []*coreauth.Auth
					for i, id := range ids {
						auth := &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: executor.routes[id], Metadata: map[string]any{
							"access_token": "synthetic-healthy", "refresh_token": "synthetic-refresh", "expired": time.Now().Add(time.Hour).Format(time.RFC3339),
							"codex_identity_namespace": fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), "codex_identity_version": 1,
						}}
						if i == 0 {
							auth.Metadata["access_token"] = "synthetic-revoked"
							if limit != 0 {
								auth.Metadata["concurrency_limit"] = limit
							}
						}
						if i == 2 {
							auth.Metadata["concurrency_limit"] = 3
							auth.Disabled, auth.Status = true, coreauth.StatusDisabled
						}
						registered, errRegister := manager.Register(ctx, auth)
						if errRegister != nil {
							t.Fatal(errRegister)
						}
						auths = append(auths, registered)
						registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					}
					wantLimit := coreauth.EffectiveConcurrencyLimit(auths[0])
					if limit == 0 && wantLimit != 10 {
						t.Fatalf("unset credential limit = %d, want 10", wantLimit)
					}
					for range wantLimit - 1 {
						release, errAcquire := manager.AcquireCredentialConcurrency(ctx, auths[0])
						if errAcquire != nil {
							t.Fatal(errAcquire)
						}
						t.Cleanup(release)
					}
					execute := func(key string) ([]byte, error) {
						opts := coreexecutor.Options{Headers: http.Header{"X-Synthetic-Key": {key}}}
						req := coreexecutor.Request{Model: model}
						if !stream {
							response, errExecute := manager.Execute(ctx, []string{"codex"}, req, opts)
							return response.Payload, errExecute
						}
						response, errStream := manager.ExecuteStream(ctx, []string{"codex"}, req, opts)
						if errStream != nil {
							return nil, errStream
						}
						var body []byte
						for chunk := range response.Chunks {
							body = append(body, chunk.Payload...)
							if chunk.Err != nil {
								return nil, chunk.Err
							}
						}
						return body, nil
					}
					reset := func(auth *coreauth.Auth) {
						t.Helper()
						before, _ := manager.GetByID(auth.ID)
						inflight, capacity := manager.CredentialConcurrencySnapshot(auth.ID)
						saves := store.saveCalls
						request, _ := json.Marshal(pluginapi.HostRoutingResetCooldownRequest{AuthIndex: auth.Index})
						if _, errReset := host.callFromPlugin(withHostCallbackPluginID(ctx, "v812-bound-scheduler"), pluginabi.MethodHostRoutingResetCooldown, request); errReset != nil {
							t.Fatal(errReset)
						}
						after, _ := manager.GetByID(auth.ID)
						if store.saveCalls != saves || !reflect.DeepEqual(before.Metadata, after.Metadata) || before.ProxyURL != after.ProxyURL || before.Disabled != after.Disabled {
							t.Fatal("reset changed token files, identity, proxy, or disabled state")
						}
						if gotInflight, gotCapacity := manager.CredentialConcurrencySnapshot(auth.ID); gotInflight != inflight || gotCapacity != capacity {
							t.Fatal("reset changed active leases or credential limit")
						}
					}
					if body, errExecute := execute("key-a"); errExecute == nil || len(body) != 0 {
						t.Fatal("revoked credential escaped its key binding")
					}
					for _, result := range []coreauth.Result{{Success: true}, {Error: &coreauth.Error{HTTPStatus: 500}}, {Error: &coreauth.Error{HTTPStatus: 429}}} {
						result.AuthID, result.Provider, result.Model = ids[0], "codex", model
						manager.MarkResult(ctx, result)
					}
					reset(auths[0])
					reset(auths[2])
					if body, errExecute := execute("key-a"); errExecute == nil || len(body) != 0 {
						t.Fatal("cooldown reset revived terminal auth or fell back outside binding")
					}
					terminal, _ := manager.GetByID(ids[0])
					if !terminal.Unavailable || terminal.Status != coreauth.StatusError || terminal.LastError == nil || terminal.LastError.HTTPStatus != 401 || !terminal.NextRetryAfter.IsZero() || !terminal.NextRefreshAfter.IsZero() {
						t.Fatal("late results or reset erased terminal unauthorized state")
					}
					if body, errExecute := execute("key-c"); errExecute == nil || len(body) != 0 {
						t.Fatal("reset enabled a disabled credential")
					}
					if body, errExecute := execute("key-b"); errExecute != nil || string(body) != ids[1] {
						t.Fatalf("independent key lost its healthy credential: %v", errExecute)
					}
					executor.mu.Lock()
					if executor.refreshCalls != 1 || executor.calls[ids[0]] != 1 || executor.calls[ids[2]] != 0 {
						t.Error("terminal or disabled credential retried unexpectedly")
					}
					executor.refreshOK = true
					executor.mu.Unlock()
					forceCtx := coreexecutor.WithRequestProxyURL(ctx, "http://execution-override.invalid:8080")
					restored, errRefresh := manager.ForceRefreshAuth(forceCtx, ids[0])
					if errRefresh != nil || restored.Unavailable || restored.ProxyURL != auths[0].ProxyURL || restored.Metadata["codex_identity_namespace"] != auths[0].Metadata["codex_identity_namespace"] {
						t.Fatalf("explicit refresh failed to recover the same credential: %v", errRefresh)
					}
					if body, errExecute := execute("key-a"); errExecute != nil || string(body) != ids[0] {
						t.Fatalf("recovered credential did not resume bound routing: %v", errExecute)
					}
					lastRelease, errAcquire := manager.AcquireCredentialConcurrency(ctx, restored)
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					t.Cleanup(lastRelease)
					reset(restored)
					if _, errOverflow := execute("key-a"); !coreauth.IsCredentialConcurrencyError(errOverflow) {
						t.Fatalf("cooldown reset bypassed occupied credential capacity: %v", errOverflow)
					}
					if gotInflight, gotLimit := manager.CredentialConcurrencySnapshot(ids[0]); gotInflight != wantLimit || gotLimit != wantLimit {
						t.Fatalf("active credential = %d/%d, want %d/%d", gotInflight, gotLimit, wantLimit, wantLimit)
					}
				})
			}
		}
	}
}

func TestRoutingResetRejectsClosedPluginInstanceV812(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	host := New()
	host.SetAuthManager(manager)
	next := time.Now().Add(time.Hour)
	auth, errRegister := manager.Register(t.Context(), &coreauth.Auth{
		ID: t.Name(), Provider: "codex", Status: coreauth.StatusError, Unavailable: true,
		NextRetryAfter: next, Quota: coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: next},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	instance := &hostCallbackInstance{}
	instance.closed.Store(true)
	ctx := withHostCallbackIdentity(t.Context(), "old-plugin-instance", instance)
	request, _ := json.Marshal(pluginapi.HostRoutingResetCooldownRequest{AuthIndex: auth.Index})
	if _, errReset := host.callFromPlugin(ctx, pluginabi.MethodHostRoutingResetCooldown, request); errReset == nil {
		t.Fatal("closed plugin instance mutated routing state")
	}
	current, _ := manager.GetByID(auth.ID)
	if !current.Unavailable || !current.Quota.Exceeded || !current.NextRetryAfter.Equal(next) {
		t.Fatal("closed callback cleared credential cooldown")
	}
}
