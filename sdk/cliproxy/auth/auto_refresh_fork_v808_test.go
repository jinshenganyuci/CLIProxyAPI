package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialconcurrency"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type fork808RefreshExecutor struct {
	schedulerProviderTestExecutor
	started chan string
	release chan struct{}
}

func (e *fork808RefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.started <- cliproxyexecutor.RequestProxyURL(ctx)
	<-e.release
	auth.Metadata["access_token"] = "refreshed-access"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

// Exercise the actual background worker while management edits or replaces a
// credential. Refresh-job bookkeeping must not undo fork controls or live leases.
func TestAutoRefreshWorkerPreservesForkControlsV808(t *testing.T) {
	for _, replace := range []bool{false, true} {
		for _, proxy := range []string{"direct", "http://selected.invalid:8080", "socks5://selected.invalid:1080"} {
			t.Run(fmt.Sprintf("replace=%t/proxy=%s", replace, proxy), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					store := newMemoryAuthTestStore()
					manager := NewManager(store, nil, nil)
					executor := &fork808RefreshExecutor{
						schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
						started:                       make(chan string, 1), release: make(chan struct{}),
					}
					manager.RegisterExecutor(executor)
					var releaseOnce sync.Once
					releaseRefresh := func() { releaseOnce.Do(func() { close(executor.release) }) }
					defer releaseRefresh()
					loop := newAuthAutoRefreshLoop(manager, refreshCheckInterval, 1)
					manager.refreshLoop = loop
					ctx, cancelCtx := context.WithCancel(context.Background())
					defer cancelCtx()
					registered, errRegister := manager.Register(ctx, &Auth{
						ID: "worker-fork", Provider: "codex", Status: StatusActive, ProxyURL: "http://original.invalid:8080",
						Metadata: map[string]any{
							"access_token": "expired-access", "refresh_token": "synthetic-refresh",
							"expired":                  time.Now().Add(-time.Hour).Format(time.RFC3339),
							"codex_identity_namespace": "11111111-1111-4111-8111-111111111111", "codex_identity_version": 1,
						},
					})
					if errRegister != nil {
						t.Fatal(errRegister)
					}
					lease, errAcquire := manager.acquireCredentialConcurrency(ctx, registered)
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					defer lease.Release()
					assertCredentialConcurrencyLifecycleSnapshot(t, manager, registered.ID, 1, 10)
					loop.rebuild(time.Now())
					loop.handleDue(ctx, time.Now())
					if len(loop.jobs) != 1 {
						t.Fatalf("queued jobs = %d, want 1", len(loop.jobs))
					}
					workerCtx := cliproxyexecutor.WithRequestProxyURL(ctx, "http://execution-override.invalid:8080")
					go loop.worker(workerCtx)
					if executionProxy := <-executor.started; executionProxy != "" {
						t.Fatal("background refresh retained an execution-only proxy override")
					}

					edited, _ := manager.GetByID(registered.ID)
					edited.ProxyURL = proxy
					edited.Metadata["proxy_url"] = proxy
					edited.Metadata["codex_identity_namespace"] = "22222222-2222-4222-8222-222222222222"
					edited.Metadata[credentialconcurrency.Field] = 3
					edited.Metadata["plugin_data"] = "retained"
					edited.Disabled, edited.Status = true, StatusDisabled
					wantToken := "refreshed-access"
					if replace {
						manager.Remove(ctx, edited.ID)
						edited.Metadata["access_token"] = "replacement-access"
						wantToken = "replacement-access"
						if _, errReplace := manager.Register(ctx, edited); errReplace != nil {
							t.Fatal(errReplace)
						}
					} else if _, errUpdate := manager.Update(ctx, edited); errUpdate != nil {
						t.Fatal(errUpdate)
					}
					releaseRefresh()
					synctest.Wait()
					current, _ := manager.GetByID(registered.ID)
					persisted, errList := store.List(ctx)
					if errList != nil || len(persisted) != 1 {
						t.Fatalf("persisted credentials = %d, error = %v", len(persisted), errList)
					}
					for _, observed := range []*Auth{current, persisted[0]} {
						if observed.Metadata["access_token"] != wantToken || observed.Metadata["refresh_token"] != "synthetic-refresh" {
							t.Fatal("background job published stale or incomplete credentials")
						}
						if observed.ProxyURL != proxy || observed.Metadata["proxy_url"] != proxy || observed.Metadata["codex_identity_namespace"] != edited.Metadata["codex_identity_namespace"] {
							t.Fatal("background job reverted the selected proxy or credential identity")
						}
						if !observed.Disabled || observed.Status != StatusDisabled || observed.Metadata["plugin_data"] != "retained" || EffectiveConcurrencyLimit(observed) != 3 {
							t.Fatal("background job reverted plugin/disabled state or concurrency limit")
						}
					}
					if current.RegistrationEpoch != edited.RegistrationEpoch {
						t.Fatal("background job reverted the accepted registration epoch")
					}
					assertCredentialConcurrencyLifecycleSnapshot(t, manager, registered.ID, 1, 3)
					if len(manager.refreshJobs) != 0 {
						t.Fatal("completed background job retained pending ownership")
					}
					lease.Release()
					assertCredentialConcurrencyLifecycleSnapshot(t, manager, registered.ID, 0, 3)
					cancelCtx()
					synctest.Wait()
				})
			})
		}
	}
}
