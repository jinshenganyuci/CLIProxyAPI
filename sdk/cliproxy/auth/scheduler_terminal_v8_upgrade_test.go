package auth

import (
	"context"
	"errors"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV8TerminalSchedulerRejectKeepsCredentialLeases(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			manager, executor, auths, models := newCredentialConcurrencyManager(t, 3, 0)
			release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), auths[0])
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			t.Cleanup(release)
			executions := 0
			executor.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				executions++
				return cliproxyexecutor.Response{}, nil
			}
			executor.stream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				executions++
				return nil, nil
			}
			scheduler := &fakePluginScheduler{handled: true, resp: pluginapi.SchedulerPickResponse{
				Handled: true, Reject: true, RejectCode: "quota_reserve_exhausted", RejectReason: "synthetic terminal rejection",
				AuthID: auths[1].ID, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst,
			}}
			manager.SetPluginScheduler(scheduler)
			req := cliproxyexecutor.Request{Model: models[0], Payload: []byte(`{"model":"test"}`)}
			var errExecute error
			switch mode {
			case "execute":
				_, errExecute = manager.Execute(context.Background(), []string{"test"}, req, cliproxyexecutor.Options{})
			case "count":
				_, errExecute = manager.ExecuteCount(context.Background(), []string{"test"}, req, cliproxyexecutor.Options{})
			case "stream":
				_, errExecute = manager.ExecuteStream(context.Background(), []string{"test"}, req, cliproxyexecutor.Options{})
			}
			var rejection *Error
			if !errors.As(errExecute, &rejection) || rejection.Code != "quota_reserve_exhausted" {
				t.Fatalf("terminal scheduler response lost: %v", errExecute)
			}
			if executions != 0 || scheduler.calls != 1 {
				t.Fatalf("terminal rejection fell back or retried: executions=%d scheduler calls=%d", executions, scheduler.calls)
			}
			for i, auth := range auths {
				want := 0
				if i == 0 {
					want = 1
				}
				requireCredentialConcurrencySnapshot(t, manager, auth.ID, want, 10)
			}
		})
	}
}
