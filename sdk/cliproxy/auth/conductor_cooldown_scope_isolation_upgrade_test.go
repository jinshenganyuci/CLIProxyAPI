package auth

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCooldownScopeUpgradeInterleavedSuccessAndPersistence(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, sameModel := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-model-%t", sameModel), func(t *testing.T) {
			ctx := context.Background()
			prefix := "scope-upgrade-" + uuid.NewString()
			models := []string{prefix + "-long", prefix + "-shared", prefix + "-healthy"}
			manager, credential := newCooldownMonotonicManager(t, models...)
			store := NewFileCooldownStateStore(t.TempDir())
			manager.SetCooldownStateStore(store)
			for _, model := range models {
				manager.MarkResult(ctx, Result{AuthID: credential.ID, Provider: credential.Provider, Model: model, Success: true})
			}
			longRetry := 8 * 24 * time.Hour
			manager.MarkResult(ctx, Result{
				AuthID: credential.ID, Provider: credential.Provider, Model: models[0], RetryAfter: &longRetry,
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic model-only quota"},
			})
			modelOnly, _ := manager.GetByID(credential.ID)
			longDeadline := modelOnly.ModelStates[models[0]].Quota.NextRecoverAt
			sharedModel := models[1]
			if sameModel {
				sharedModel = models[0]
			}
			sharedRetry := 30 * time.Minute
			beforeShared := time.Now()
			manager.MarkResult(ctx, Result{
				AuthID: credential.ID, Provider: credential.Provider, Model: sharedModel,
				RetryAfter: &sharedRetry, CredentialScope: true,
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic shared quota"},
			})
			afterShared := time.Now()
			shared, _ := manager.GetByID(credential.ID)
			deadline := shared.Quota.NextRecoverAt
			if deadline.Before(beforeShared.Add(sharedRetry)) || deadline.After(afterShared.Add(sharedRetry)) {
				t.Fatalf("credential inherited model deadline: got=%v, long=%v", deadline, longDeadline)
			}
			// Responses already in flight may succeed after the shared rejection arrives.
			for _, model := range append(models, "") {
				manager.MarkResult(ctx, Result{AuthID: credential.ID, Provider: credential.Provider, Model: model, Success: true})
				current, _ := manager.GetByID(credential.ID)
				if current.Quota.Reason != "credential_quota" || !current.Quota.Exceeded || !current.Quota.NextRecoverAt.Equal(deadline) {
					t.Fatalf("success on %q cleared or changed credential quota: %+v", model, current.Quota)
				}
				if !current.ModelStates[models[0]].Quota.NextRecoverAt.Equal(longDeadline) {
					t.Fatalf("concurrent success on %q cleared long model quota", model)
				}
			}
			shortRetry := time.Minute
			manager.MarkResult(ctx, Result{
				AuthID: credential.ID, Provider: credential.Provider, Model: models[1], RetryAfter: &shortRetry,
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic shorter model quota"},
			})
			current, _ := manager.GetByID(credential.ID)
			if current.Quota.Reason != "credential_quota" || !current.Quota.NextRecoverAt.Equal(deadline) {
				t.Fatalf("model-only failure changed active credential deadline: %+v", current.Quota)
			}
			// Restore the real file-backed cooldown records into a fresh manager.
			restored := NewManager(nil, nil, nil)
			if _, errRegister := restored.Register(ctx, &Auth{ID: credential.ID, Provider: credential.Provider}); errRegister != nil {
				t.Fatal(errRegister)
			}
			restored.SetCooldownStateStore(store)
			if errRestore := restored.RestoreCooldownStates(ctx); errRestore != nil {
				t.Fatal(errRestore)
			}
			persisted, _ := restored.GetByID(credential.ID)
			if persisted.Quota.Reason != "credential_quota" || !persisted.Quota.NextRecoverAt.Equal(deadline) {
				t.Fatalf("restart changed credential scope/deadline: %+v", persisted.Quota)
			}
			for _, snapshot := range []*Auth{current, persisted} {
				for _, model := range models {
					if blocked, _, _ := isAuthBlockedForModel(snapshot, model, beforeShared); !blocked {
						t.Fatalf("model %q bypassed active shared cooldown", model)
					}
				}
				later := deadline.Add(time.Minute)
				if blocked, _, next := isAuthBlockedForModel(snapshot, models[2], later); blocked {
					t.Fatalf("healthy sibling inherited long model quota: next=%v", next)
				}
				if blocked, _, next := isAuthBlockedForModel(snapshot, models[0], later); !blocked || !next.Equal(longDeadline) {
					t.Fatalf("long model quota was lost: blocked=%t next=%v want=%v", blocked, next, longDeadline)
				}
			}
		})
	}
}

func TestCooldownScopeUpgradeLocalCapacityLeavesSupplierStateUnchanged(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			manager, executor, credentials, models := newCredentialConcurrencyManager(t, 1, 0)
			ctx := context.Background()
			credential := credentials[0]
			longRetry := 8 * 24 * time.Hour
			manager.MarkResult(ctx, Result{
				AuthID: credential.ID, Provider: credential.Provider, Model: models[0], RetryAfter: &longRetry,
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic model quota"},
			})
			before, _ := manager.GetByID(credential.ID)
			for range 10 {
				release, errAcquire := manager.AcquireCredentialConcurrency(ctx, credential)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				t.Cleanup(release)
			}
			executor.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				t.Error("full credential reached upstream executor")
				return cliproxyexecutor.Response{}, nil
			}
			executor.stream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				t.Error("full credential reached upstream stream executor")
				return nil, nil
			}
			request := cliproxyexecutor.Request{Model: models[1], Payload: []byte(`{}`)}
			var errRun error
			switch mode {
			case "count":
				_, errRun = manager.ExecuteCount(ctx, []string{credential.Provider}, request, cliproxyexecutor.Options{})
			case "stream":
				_, errRun = manager.ExecuteStream(ctx, []string{credential.Provider}, request, cliproxyexecutor.Options{})
			default:
				_, errRun = manager.Execute(ctx, []string{credential.Provider}, request, cliproxyexecutor.Options{})
			}
			if !IsCredentialConcurrencyError(errRun) || statusCodeFromError(errRun) != http.StatusTooManyRequests {
				t.Fatalf("capacity refusal = %v, want typed local 429", errRun)
			}
			after, _ := manager.GetByID(credential.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("local capacity refusal changed supplier state: before=%+v after=%+v", before, after)
			}
			requireCredentialConcurrencySnapshot(t, manager, credential.ID, 10, 10)
		})
	}
}
