package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSessionAffinityNeverPreservesLocalConcurrencyAndCancelRelease(t *testing.T) {
	for _, tc := range []struct {
		name, mode       string
		weighted, pinned bool
	}{
		{name: "single-selection", mode: "single-selection"},
		{name: "mixed-execution", mode: "mixed-execution"},
		{name: "weighted-pinned-single-selection", mode: "single-selection", weighted: true, pinned: true},
		{name: "weighted-mixed-execution", mode: "mixed-execution", weighted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, executor, auths, models := newCredentialConcurrencyManager(t, 2, 1)
			manager.SetRetryConfig(3, 0, 0)
			var fallback Selector = &FillFirstSelector{}
			if tc.weighted {
				fallback = &WeightedRoundRobinSelector{}
			}
			affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback: fallback, TTL: time.Hour, RebindOn: SessionAffinityRebindOnNever,
			})
			defer affinity.Stop()
			manager.SetSelector(affinity)
			options := func(block bool) cliproxyexecutor.Options {
				metadata := map[string]any{"block": block}
				if tc.pinned {
					metadata[cliproxyexecutor.PinnedAuthMetadataKey] = auths[0].ID
				}
				return cliproxyexecutor.Options{
					Headers:  http.Header{"Session-Id": []string{"never-capacity-session"}},
					Metadata: metadata,
				}
			}
			selected, _, errBind := manager.pickNext(context.Background(), "test", models[0], options(false), nil)
			if errBind != nil || selected == nil || selected.ID != auths[0].ID {
				t.Fatalf("initial binding = %v, %v; want %s", selected, errBind, auths[0].ID)
			}
			entered := make(chan string, 1)
			var calls, alternateCalls atomic.Int32
			executor.execute = func(ctx context.Context, credential *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls.Add(1)
				if credential.ID != auths[0].ID {
					alternateCalls.Add(1)
				}
				if block, _ := opts.Metadata["block"].(bool); block {
					entered <- credential.ID
					<-ctx.Done()
					return cliproxyexecutor.Response{}, ctx.Err()
				}
				return cliproxyexecutor.Response{Payload: []byte(credential.ID)}, nil
			}
			request := cliproxyexecutor.Request{Model: models[0]}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, errExecute := manager.Execute(ctx, []string{"test"}, request, options(true))
				result <- errExecute
			}()
			t.Cleanup(func() {
				cancel()
				awaitCredentialConcurrencySignal(t, finished)
			})
			if id := awaitCredentialConcurrencySignal(t, entered); id != auths[0].ID {
				t.Fatalf("held credential = %s, want %s", id, auths[0].ID)
			}
			requireCredentialConcurrencySnapshot(t, manager, auths[0].ID, 1, 1)
			requireCredentialConcurrencySnapshot(t, manager, auths[1].ID, 0, 1)

			attempt := func() (string, error) {
				if tc.mode == "single-selection" {
					credential, release, errSelect := manager.selectAuthWithConcurrency(context.Background(), "test", models[0], options(false))
					if release != nil {
						defer release()
					}
					if credential != nil {
						return credential.ID, errSelect
					}
					return "", errSelect
				}
				response, errExecute := manager.Execute(context.Background(), []string{"test"}, request, options(false))
				return string(response.Payload), errExecute
			}
			if id, errBusy := attempt(); id != "" || !IsCredentialConcurrencyError(errBusy) || statusCodeFromError(errBusy) != http.StatusTooManyRequests {
				t.Fatalf("bound busy request = %q, %v; want local 429 without failover", id, errBusy)
			}
			if calls.Load() != 1 || alternateCalls.Load() != 0 {
				t.Fatalf("executor calls = %d, alternate = %d; want only the held request", calls.Load(), alternateCalls.Load())
			}
			current, _ := manager.GetByID(auths[0].ID)
			if current.Unavailable || current.LastError != nil {
				t.Fatalf("local capacity rejection changed credential health: %+v", current)
			}
			cancel()
			if errCanceled := awaitCredentialConcurrencySignal(t, result); !errors.Is(errCanceled, context.Canceled) {
				t.Fatalf("held request cancellation = %v", errCanceled)
			}
			requireCredentialConcurrencySnapshot(t, manager, auths[0].ID, 0, 1)
			if id, errRecovered := attempt(); errRecovered != nil || id != auths[0].ID {
				t.Fatalf("request after cancellation = %q, %v; want original bound credential", id, errRecovered)
			}
			if alternateCalls.Load() != 0 {
				t.Fatal("never affinity moved to the idle alternative credential")
			}
			requireCredentialConcurrencySnapshot(t, manager, auths[0].ID, 0, 1)
			requireCredentialConcurrencySnapshot(t, manager, auths[1].ID, 0, 1)
		})
	}
}
