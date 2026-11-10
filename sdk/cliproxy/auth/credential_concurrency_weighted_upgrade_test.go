package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCredentialConcurrencyWeightedZeroPriorityDoesNotHideCapacity(t *testing.T) {
	for _, mode := range []string{"single-selection", "mixed-plugin-fallback"} {
		t.Run(mode, func(t *testing.T) {
			manager, executor, auths, models := newCredentialConcurrencyManager(t, 2, 1)
			manager.SetSelector(&WeightedRoundRobinSelector{})
			zero := auths[0].Clone()
			zero.Attributes = map[string]string{AttributeWeight: "0", "priority": "100"}
			if _, errUpdate := manager.Update(t.Context(), zero); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			if mode == "mixed-plugin-fallback" {
				// A plugin declining selection exercises the real mixed legacy path.
				manager.SetPluginScheduler(&fakePluginScheduler{
					pick: func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
						return pluginapi.SchedulerPickResponse{}, false, nil
					},
				})
			}
			calls := 0
			executor.execute = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls++
				requireCredentialConcurrencySnapshot(t, manager, auth.ID, 1, 1)
				return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
			}
			attempt := func() (string, error) {
				if mode == "single-selection" {
					selected, release, errSelect := manager.selectAuthWithConcurrency(t.Context(), "test", models[0], cliproxyexecutor.Options{})
					if release != nil {
						defer release()
					}
					if selected != nil {
						requireCredentialConcurrencySnapshot(t, manager, selected.ID, 1, 1)
						return selected.ID, errSelect
					}
					return "", errSelect
				}
				response, errExecute := manager.Execute(t.Context(), []string{"test"}, cliproxyexecutor.Request{Model: models[0]}, cliproxyexecutor.Options{})
				return string(response.Payload), errExecute
			}
			if id, errSelect := attempt(); errSelect != nil || id != auths[1].ID {
				t.Fatalf("selection = %q, %v; want lower-priority positive-weight credential", id, errSelect)
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), auths[1])
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer release()
			callsBefore := calls
			if id, errBusy := attempt(); id != "" || !IsCredentialConcurrencyError(errBusy) || statusCodeFromError(errBusy) != http.StatusTooManyRequests {
				t.Fatalf("positive-weight pool full = %q, %v; want local 429", id, errBusy)
			}
			if calls != callsBefore {
				t.Fatal("capacity rejection executed an upstream request")
			}
			for _, auth := range auths {
				current, _ := manager.GetByID(auth.ID)
				if current.Unavailable || current.LastError != nil || current.Failed != 0 || current.Quota.Exceeded {
					t.Fatalf("capacity rejection changed credential health: %+v", current)
				}
			}
			release()
			for _, auth := range auths {
				requireCredentialConcurrencySnapshot(t, manager, auth.ID, 0, 1)
			}
		})
	}
}
