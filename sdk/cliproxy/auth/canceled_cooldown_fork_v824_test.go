package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestV824CanceledCooldownRespectsCredentialVersionBindingAndLease(t *testing.T) {
	for _, bootstrap := range []bool{false, true} {
		for _, replace := range []bool{false, true} {
			for _, quota := range []bool{false, true} {
				t.Run(fmt.Sprintf("bootstrap=%t/replaced=%t/quota=%t", bootstrap, replace, quota), func(t *testing.T) {
					manager, exec, auths, models := newCredentialConcurrencyManager(t, 3, 0)
					plugin := &credentialConcurrencyPlugin{allowed: map[string]map[string]bool{"key-a": {auths[0].ID: true}, "key-b": {auths[1].ID: true}, "key-c": {auths[2].ID: true}}}
					manager.SetPluginScheduler(plugin)
					entered, release := make(chan struct{}), make(chan struct{})
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					exec.stream = func(execCtx context.Context, a *Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
						if a.ID != auths[0].ID {
							t.Errorf("bound credential changed to %s", a.ID)
						}
						close(entered)
						<-release
						cancel()
						var err error = context.Canceled
						if quota {
							err = issue6503QuotaError{retryAfter: 10 * time.Minute}
						}
						if !bootstrap {
							return nil, err
						}
						chunks := make(chan coreexecutor.StreamChunk, 1)
						chunks <- coreexecutor.StreamChunk{Err: err}
						close(chunks)
						return &coreexecutor.StreamResult{Chunks: chunks}, nil
					}
					held, err := manager.AcquireCredentialConcurrency(context.Background(), auths[0])
					if err != nil {
						t.Fatal(err)
					}
					defer held()
					done := make(chan error, 1)
					go func() {
						_, err := manager.ExecuteStream(ctx, []string{"test"}, coreexecutor.Request{Model: models[0]}, credentialConcurrencyOptions("key-a"))
						done <- err
					}()
					awaitCredentialConcurrencySignal(t, entered)
					requireCredentialConcurrencySnapshot(t, manager, auths[0].ID, 2, 10)
					if replace {
						fresh := auths[0].Clone()
						fresh.Metadata["access_token"] = "accepted-token"
						fresh.Metadata["concurrency_limit"] = 3
						fresh.Metadata["codex_identity_namespace"] = "accepted-namespace"
						fresh.ProxyURL = "direct"
						if _, err := manager.Update(context.Background(), fresh); err != nil {
							t.Fatal(err)
						}
					}
					close(release)
					if err := awaitCredentialConcurrencySignal(t, done); !errors.Is(err, context.Canceled) {
						t.Fatalf("result=%v, want canceled", err)
					}
					limit := 10
					if replace {
						limit = 3
					}
					requireCredentialConcurrencySnapshot(t, manager, auths[0].ID, 1, limit)
					current, _ := manager.GetByID(auths[0].ID)
					wantCooldown := quota && !replace
					if current.Unavailable != wantCooldown || current.Quota.Exceeded != wantCooldown || current.NextRetryAfter.After(time.Now().Add(5*time.Minute)) != wantCooldown {
						t.Fatalf("cooldown state mismatched: unavailable=%t quota=%t next=%v", current.Unavailable, current.Quota.Exceeded, current.NextRetryAfter)
					}
					if replace && (current.Metadata["access_token"] != "accepted-token" || current.Metadata["codex_identity_namespace"] != "accepted-namespace" || current.ProxyURL != "direct") {
						t.Fatal("canceled result overwrote replacement metadata")
					}
					for _, other := range auths[1:] {
						a, _ := manager.GetByID(other.ID)
						requireCredentialConcurrencySnapshot(t, manager, a.ID, 0, 10)
						if a.Unavailable || a.Failed != 0 || a.Quota.Exceeded {
							t.Fatal("cancellation touched sibling credential")
						}
					}
					if plugin.callCount() != 1 {
						t.Errorf("canceled request invoked scheduler %d times", plugin.callCount())
					}
					held()
					requireCredentialConcurrencySnapshot(t, manager, auths[0].ID, 0, limit)
				})
			}
		}
	}
}
