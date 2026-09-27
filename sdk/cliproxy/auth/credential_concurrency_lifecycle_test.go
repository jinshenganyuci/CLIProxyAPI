package auth

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"reflect"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialconcurrency"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestEffectiveConcurrencyLimitAndValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		auth    *Auth
		limit   int
		invalid bool
	}{
		{name: "nil", limit: 10},
		{name: "missing", auth: &Auth{}, limit: 10},
		{name: "null", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: nil}}, limit: 10},
		{name: "metadata", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: 3}}, limit: 3},
		{name: "json number", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: json.Number("3")}}, limit: 3},
		{name: "json float", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: float64(3)}}, limit: 3},
		{name: "metadata string", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: "3"}}, limit: 3},
		{name: "attribute", auth: &Auth{Attributes: map[string]string{credentialconcurrency.Field: " 3 "}}, limit: 3},
		{name: "metadata takes precedence", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: 3}, Attributes: map[string]string{credentialconcurrency.Field: "7"}}, limit: 3},
		{name: "null resets stale attribute", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: nil}, Attributes: map[string]string{credentialconcurrency.Field: "3"}}, limit: 10},
		{name: "zero", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: 0}}, limit: 10, invalid: true},
		{name: "negative", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: -1}}, limit: 10, invalid: true},
		{name: "fraction", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: 3.5}}, limit: 10, invalid: true},
		{name: "overflow", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: uint64(math.MaxUint64)}}, limit: 10, invalid: true},
		{name: "nan", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: math.NaN()}}, limit: 10, invalid: true},
		{name: "infinity", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: math.Inf(1)}}, limit: 10, invalid: true},
		{name: "boolean", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: true}}, limit: 10, invalid: true},
		{name: "invalid metadata does not use attribute", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: "invalid"}, Attributes: map[string]string{credentialconcurrency.Field: "3"}}, limit: 10, invalid: true},
		{name: "empty attribute", auth: &Auth{Attributes: map[string]string{credentialconcurrency.Field: ""}}, limit: 10, invalid: true},
		{name: "invalid attribute", auth: &Auth{Attributes: map[string]string{credentialconcurrency.Field: "3.5"}}, limit: 10, invalid: true},
		{name: "invalid shadowed attribute", auth: &Auth{Metadata: map[string]any{credentialconcurrency.Field: 3}, Attributes: map[string]string{credentialconcurrency.Field: "0"}}, limit: 3, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := EffectiveConcurrencyLimit(test.auth); got != test.limit {
				t.Fatalf("effective limit = %d, want %d", got, test.limit)
			}
			if errValidate := ValidateAuthConcurrencyLimit(test.auth); (errValidate != nil) != test.invalid {
				t.Fatalf("validation error = %v, want invalid=%t", errValidate, test.invalid)
			}
		})
	}
}

func credentialConcurrencyLifecycleManager(t *testing.T, limit int) (*Manager, *Auth) {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive}
	if limit != 0 {
		auth.Metadata = map[string]any{credentialconcurrency.Field: limit}
	}
	registered, errRegister := manager.Register(WithSkipPersist(context.Background()), auth)
	if errRegister != nil {
		t.Fatalf("register credential: %v", errRegister)
	}
	return manager, registered
}

func assertCredentialConcurrencyLifecycleSnapshot(t *testing.T, manager *Manager, id string, inflight, limit int) {
	t.Helper()
	if gotInflight, gotLimit := manager.CredentialConcurrencySnapshot(id); gotInflight != inflight || gotLimit != limit {
		t.Fatalf("concurrency snapshot = %d/%d, want %d/%d", gotInflight, gotLimit, inflight, limit)
	}
}

func TestValidateAuthConcurrencyLimitRejectsSDKMutations(t *testing.T) {
	manager, auth := credentialConcurrencyLifecycleManager(t, 3)
	for _, invalid := range []any{0, -3, 1.5, "wrong", true, credentialconcurrency.MaxLimit + 1} {
		candidate := auth.Clone()
		candidate.Metadata[credentialconcurrency.Field] = invalid
		if _, errUpdate := manager.Update(context.Background(), candidate); errUpdate == nil {
			t.Fatalf("Update accepted invalid limit %#v", invalid)
		}
		assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 0, 3)
		candidate.ID += "-invalid"
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister == nil {
			t.Fatalf("Register accepted invalid limit %#v", invalid)
		}
		if _, exists := manager.GetByID(candidate.ID); exists {
			t.Fatal("invalid registration created a credential")
		}
	}
}

func TestCredentialConcurrencyAtomicAdmissionNeverExceedsLimit(t *testing.T) {
	manager, auth := credentialConcurrencyLifecycleManager(t, 3)
	const contenders = 96
	start := make(chan struct{})
	type outcome struct {
		lease *credentialConcurrencyLease
		err   error
	}
	results := make(chan outcome, contenders)
	for range contenders {
		go func() {
			<-start
			lease, errAcquire := manager.acquireCredentialConcurrency(context.Background(), auth)
			results <- outcome{lease: lease, err: errAcquire}
		}()
	}
	close(start)
	var leases []*credentialConcurrencyLease
	for range contenders {
		result := <-results
		if result.err == nil {
			leases = append(leases, result.lease)
			t.Cleanup(result.lease.Release)
		} else if !IsCredentialConcurrencyError(result.err) || statusCodeFromError(result.err) != http.StatusTooManyRequests {
			t.Errorf("admission rejection = %v, want concurrency 429", result.err)
		}
	}
	if len(leases) != 3 {
		t.Fatalf("successful simultaneous admissions = %d, want 3", len(leases))
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 3, 3)
	var releases sync.WaitGroup
	for _, lease := range leases {
		for range 8 {
			releases.Go(lease.Release)
		}
	}
	releases.Wait()
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 0, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, errAcquire := manager.acquireCredentialConcurrency(ctx, auth); !errors.Is(errAcquire, context.Canceled) {
		t.Fatalf("canceled admission error = %v, want context.Canceled", errAcquire)
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 0, 3)
}

func TestCredentialConcurrencyLoweringLimitRetainsExistingRequests(t *testing.T) {
	manager, auth := credentialConcurrencyLifecycleManager(t, 0)
	var leases []*credentialConcurrencyLease
	for range 5 {
		lease, errAcquire := manager.acquireCredentialConcurrency(context.Background(), auth)
		if errAcquire != nil {
			t.Fatalf("initial admission: %v", errAcquire)
		}
		leases = append(leases, lease)
		t.Cleanup(lease.Release)
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 5, 10)
	updated := auth.Clone()
	updated.Metadata = map[string]any{credentialconcurrency.Field: 3}
	if _, errUpdate := manager.Update(context.Background(), updated); errUpdate != nil {
		t.Fatalf("lower limit: %v", errUpdate)
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 5, 3)
	for remaining := 5; remaining >= 3; remaining-- {
		lease, errAcquire := manager.acquireCredentialConcurrency(context.Background(), auth)
		if lease != nil {
			lease.Release()
		}
		if !IsCredentialConcurrencyError(errAcquire) {
			t.Fatalf("admission with %d existing requests = %v, want concurrency rejection", remaining, errAcquire)
		}
		leases[5-remaining].Release()
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 2, 3)
	lease, errAcquire := manager.acquireCredentialConcurrency(context.Background(), auth)
	if errAcquire != nil {
		t.Fatalf("admission after draining below new limit: %v", errAcquire)
	}
	t.Cleanup(lease.Release)
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 3, 3)
	for _, held := range leases {
		held.Release()
	}
	lease.Release()
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 0, 3)
}

func TestCredentialConcurrencyReRegistrationPreservesLeasesAndRejectsStaleEpoch(t *testing.T) {
	manager, oldAuth := credentialConcurrencyLifecycleManager(t, 3)
	var oldLeases []*credentialConcurrencyLease
	for range 3 {
		lease, errAcquire := manager.acquireCredentialConcurrency(context.Background(), oldAuth)
		if errAcquire != nil {
			t.Fatalf("initial admission: %v", errAcquire)
		}
		oldLeases = append(oldLeases, lease)
		t.Cleanup(lease.Release)
	}
	manager.Remove(context.Background(), oldAuth.ID)
	if _, errAcquire := manager.acquireCredentialConcurrency(context.Background(), oldAuth); errAcquire == nil {
		t.Fatal("deleted credential accepted a new request")
	}
	replacement, errRegister := manager.Register(context.Background(), oldAuth.Clone())
	if errRegister != nil {
		t.Fatalf("register replacement: %v", errRegister)
	}
	if replacement.RegistrationEpoch == oldAuth.RegistrationEpoch {
		t.Fatal("replacement retained stale registration epoch")
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, replacement.ID, 3, 3)
	if _, errAcquire := manager.acquireCredentialConcurrency(context.Background(), oldAuth); errAcquire == nil || IsCredentialConcurrencyError(errAcquire) {
		t.Fatalf("stale epoch admission = %v, want unavailable credential", errAcquire)
	}
	if _, errAcquire := manager.acquireCredentialConcurrency(context.Background(), replacement); !IsCredentialConcurrencyError(errAcquire) {
		t.Fatalf("replacement bypassed old active leases: %v", errAcquire)
	}
	oldLeases[0].Release()
	newLease, errAcquire := manager.acquireCredentialConcurrency(context.Background(), replacement)
	if errAcquire != nil {
		t.Fatalf("replacement admission after old request completed: %v", errAcquire)
	}
	t.Cleanup(newLease.Release)
	oldLeases[0].Release()
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, replacement.ID, 3, 3)
	for _, lease := range oldLeases {
		lease.Release()
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, replacement.ID, 1, 3)
	newLease.Release()
	newLease.Release()
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, replacement.ID, 0, 3)
}

func TestMergeAuthConcurrencyLimitRemainsUserControlled(t *testing.T) {
	for _, path := range []struct {
		name   string
		update func(*Manager, context.Context, *Auth, *Auth) (*Auth, error)
	}{
		{name: "prepare", update: (*Manager).UpdatePreparedAuth},
		{name: "refresh", update: (*Manager).UpdateRefreshedAuth},
	} {
		for _, scenario := range []struct {
			name         string
			original     int
			userLimit    int
			executorOmit bool
		}{
			{name: "executor omits existing limit", original: 3, executorOmit: true},
			{name: "executor changes existing limit", original: 3},
			{name: "executor injects limit", original: 0},
			{name: "concurrent user edit survives executor change", original: 3, userLimit: 2},
			{name: "concurrent user edit survives executor omission", original: 3, userLimit: 2, executorOmit: true},
			{name: "concurrent user reset survives stale executor", original: 3, userLimit: -1},
		} {
			t.Run(path.name+"/"+scenario.name, func(t *testing.T) {
				manager, auth := credentialConcurrencyLifecycleManager(t, scenario.original)
				if scenario.original != 0 {
					auth.Attributes = map[string]string{credentialconcurrency.Field: "3"}
					var errUpdate error
					auth, errUpdate = manager.Update(context.Background(), auth)
					if errUpdate != nil {
						t.Fatalf("set original attribute: %v", errUpdate)
					}
				}
				base := auth.Clone()
				current := auth.Clone()
				if scenario.userLimit != 0 {
					if scenario.userLimit < 0 {
						delete(current.Metadata, credentialconcurrency.Field)
						delete(current.Attributes, credentialconcurrency.Field)
					} else {
						current.Metadata[credentialconcurrency.Field] = scenario.userLimit
						current.Attributes[credentialconcurrency.Field] = "2"
					}
					var errUpdate error
					current, errUpdate = manager.Update(context.Background(), current)
					if errUpdate != nil {
						t.Fatalf("concurrent user update: %v", errUpdate)
					}
				}
				updated := base.Clone()
				updated.Metadata = map[string]any{"access_token": "new-synthetic-token"}
				updated.Attributes = map[string]string{"prepared": "yes"}
				if !scenario.executorOmit {
					updated.Metadata[credentialconcurrency.Field] = 99
					updated.Attributes[credentialconcurrency.Field] = "99"
				}
				merged, errMerge := path.update(manager, context.Background(), base, updated)
				if errMerge != nil {
					t.Fatalf("merge executor update: %v", errMerge)
				}
				if !reflect.DeepEqual(merged.Metadata[credentialconcurrency.Field], current.Metadata[credentialconcurrency.Field]) || merged.Attributes[credentialconcurrency.Field] != current.Attributes[credentialconcurrency.Field] {
					t.Fatalf("executor altered control values: metadata=%v attribute=%q, want %v / %q", merged.Metadata[credentialconcurrency.Field], merged.Attributes[credentialconcurrency.Field], current.Metadata[credentialconcurrency.Field], current.Attributes[credentialconcurrency.Field])
				}
				if merged.Metadata["access_token"] != "new-synthetic-token" || merged.Attributes["prepared"] != "yes" {
					t.Fatal("protecting the limit discarded unrelated executor updates")
				}
				assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 0, EffectiveConcurrencyLimit(current))
			})
		}
	}
}

func TestCredentialConcurrencyHomeBypassesLocalAdmission(t *testing.T) {
	manager, auth := credentialConcurrencyLifecycleManager(t, 3)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	var releases []func()
	for range 12 {
		release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), auth)
		if errAcquire != nil {
			t.Fatalf("Home request unexpectedly used local cap: %v", errAcquire)
		}
		releases = append(releases, release)
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 0, 3)
	for _, release := range releases {
		release()
		release()
	}
	assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 0, 3)
}

type credentialConcurrencyLifecycleExecutor struct {
	unauthorizedRefreshExecutor
	prepare func(context.Context, *Auth) (*Auth, error)
	execute func(context.Context, *Auth) (cliproxyexecutor.Response, error)
	refresh func(context.Context, *Auth) (*Auth, error)
}

func (e *credentialConcurrencyLifecycleExecutor) ShouldPrepareRequestAuth(*Auth) bool {
	return e.prepare != nil
}

func (e *credentialConcurrencyLifecycleExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	return e.prepare(ctx, auth)
}

func (e *credentialConcurrencyLifecycleExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.execute(ctx, auth)
}

func (e *credentialConcurrencyLifecycleExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return e.refresh(ctx, auth)
}

func TestCredentialConcurrencyExecuteReleasesAcrossPreparationInterceptionAndRefresh(t *testing.T) {
	for _, path := range []string{"prepare failure", "interceptor rejection", "401 refresh retry"} {
		t.Run(path, func(t *testing.T) {
			manager, auth := credentialConcurrencyLifecycleManager(t, 1)
			auth.Metadata["access_token"] = "synthetic-old-token"
			auth.Metadata["refresh_token"] = "synthetic-refresh-token"
			var errUpdate error
			auth, errUpdate = manager.Update(context.Background(), auth)
			if errUpdate != nil {
				t.Fatalf("set token fixture: %v", errUpdate)
			}
			const model = "gpt-concurrency-lifecycle"
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			executor := &credentialConcurrencyLifecycleExecutor{unauthorizedRefreshExecutor: unauthorizedRefreshExecutor{id: "codex"}}
			manager.RegisterExecutor(executor)
			executions, preparations, refreshes, interceptions := 0, 0, 0, 0
			assertHeld := func() {
				assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 1, 1)
				lease, errAcquire := manager.acquireCredentialConcurrency(context.Background(), auth)
				if lease != nil {
					lease.Release()
				}
				if !IsCredentialConcurrencyError(errAcquire) {
					t.Fatalf("active lifecycle step failed to reserve its slot: %v", errAcquire)
				}
			}
			executor.execute = func(_ context.Context, current *Auth) (cliproxyexecutor.Response, error) {
				executions++
				assertHeld()
				if path == "401 refresh retry" && executions == 1 {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "synthetic expired token"}
				}
				if current.Metadata["access_token"] != "synthetic-fresh-token" {
					t.Errorf("retry token = %v, want refreshed token", current.Metadata["access_token"])
				}
				return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
			}
			executor.refresh = func(_ context.Context, current *Auth) (*Auth, error) {
				refreshes++
				assertHeld()
				updated := current.Clone()
				updated.Metadata["access_token"] = "synthetic-fresh-token"
				return updated, nil
			}
			opts := cliproxyexecutor.Options{}
			if path == "prepare failure" {
				executor.prepare = func(context.Context, *Auth) (*Auth, error) {
					preparations++
					assertHeld()
					return nil, &Error{HTTPStatus: http.StatusBadRequest, Message: "synthetic preparation failure"}
				}
			}
			if path == "interceptor rejection" {
				opts.RequestAfterAuthInterceptor = func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					interceptions++
					assertHeld()
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{Terminate: true, StatusCode: http.StatusForbidden, ResponseBody: []byte("synthetic plugin rejection")}
				}
			}
			response, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
			if path == "401 refresh retry" {
				if errExecute != nil || string(response.Payload) != "ok" || executions != 2 || refreshes != 1 {
					t.Fatalf("refresh lifecycle: error=%v payload=%q executions=%d refreshes=%d", errExecute, response.Payload, executions, refreshes)
				}
			} else if errExecute == nil || executions != 0 || preparations+interceptions != 1 {
				t.Fatalf("rejected lifecycle: error=%v executions=%d prepare=%d intercept=%d", errExecute, executions, preparations, interceptions)
			}
			assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 0, 1)
			current, exists := manager.GetByID(auth.ID)
			if !exists {
				t.Fatal("credential disappeared after execution")
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), current)
			if errAcquire != nil {
				t.Fatalf("completed execution leaked its slot: %v", errAcquire)
			}
			release()
			assertCredentialConcurrencyLifecycleSnapshot(t, manager, auth.ID, 0, 1)
		})
	}
}
