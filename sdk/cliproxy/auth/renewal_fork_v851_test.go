package auth

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestV851ExpiredAccessReplacementRetainsSignInGate(t *testing.T) {
	for _, path := range []string{"update", "metadata"} {
		t.Run(path, func(t *testing.T) {
			m, _, base := newMetadataControlsFixture(t, nil)
			base.Metadata["refresh_token"] = "rejected-refresh"
			base.Metadata["expired"] = "2000-01-01T00:00:00Z"
			base.Metadata["codex_identity_namespace"] = "stable-v851"
			base.Metadata["concurrency_limit"] = 3
			var err error
			base, err = m.Update(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			release, err := m.AcquireCredentialConcurrency(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			m.mu.Lock()
			current := m.auths[base.ID]
			current.RequiresSignIn = true
			current.RejectedRefreshToken = "rejected-refresh"
			current.RefreshError = &Error{Code: "refresh_token_reused", Message: "sign-in required", HTTPStatus: http.StatusUnauthorized}
			base = current.Clone()
			m.mu.Unlock()
			edit := func(a *Auth) error {
				a.Metadata["access_token"] = "replacement-expired-access"
				a.ProxyURL = "direct"
				a.Metadata["proxy_url"] = "direct"
				return nil
			}
			var got *Auth
			if path == "metadata" {
				got, err = m.UpdateAndPersistMetadata(t.Context(), base.ID, edit)
			} else {
				_ = edit(base)
				got, err = m.Update(t.Context(), base)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !got.RequiresSignIn || got.RejectedRefreshToken != "rejected-refresh" || !got.Unavailable || got.Status != StatusError || got.LastError == nil || got.LastError.HTTPStatus != http.StatusUnauthorized {
				t.Fatalf("expired access replacement escaped renewal gate: requiresSignIn=%v unavailable=%v status=%s lastError=%v", got.RequiresSignIn, got.Unavailable, got.Status, got.LastError)
			}
			if got.RegistrationEpoch != base.RegistrationEpoch || got.Metadata["codex_identity_namespace"] != "stable-v851" || got.ProxyURL != "direct" {
				t.Fatal("renewal recovery changed identity or accepted proxy")
			}
			requireCredentialConcurrencySnapshot(t, m, base.ID, 1, 3)
		})
	}
}

type v851BlockingMetadataStore struct {
	Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *v851BlockingMetadataStore) Save(ctx context.Context, a *Auth) (string, error) {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	return s.Store.Save(ctx, a)
}

func TestV851NoOpRejectedRefreshWaitsForDurableReplacement(t *testing.T) {
	m, store, base := newMetadataControlsFixture(t, nil)
	base.Metadata["refresh_token"] = "rejected-refresh"
	base.Metadata["expired"] = "2100-01-01T00:00:00Z"
	base.Metadata["codex_identity_namespace"] = "stable-v851"
	var err error
	base, err = m.Update(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	current := m.auths[base.ID]
	current.RequiresSignIn = true
	current.RejectedRefreshToken = "rejected-refresh"
	current.RefreshError = &Error{Code: "refresh_token_reused", Message: "sign-in required", HTTPStatus: http.StatusUnauthorized}
	base = current.Clone()
	m.mu.Unlock()

	saveGate := &v851BlockingMetadataStore{Store: store, started: make(chan struct{}), release: make(chan struct{})}
	m.SetStore(saveGate)
	var releaseSave sync.Once
	defer releaseSave.Do(func() { close(saveGate.release) })
	refreshStarted := make(chan struct{})
	refreshRelease := make(chan struct{})
	var releaseRefresh sync.Once
	defer releaseRefresh.Do(func() { close(refreshRelease) })
	executor := &unauthorizedRefreshExecutor{
		id:            "codex",
		refreshTokens: map[string]string{base.ID: "old-token"},
		onRefresh: func() {
			close(refreshStarted)
			<-refreshRelease
		},
	}
	m.RegisterExecutor(executor)
	refreshDone := make(chan error, 1)
	go func() {
		_, errRefresh := m.refreshAuthForRequestAtEpoch(withForceRefresh(t.Context()), base.ID, "", base.RegistrationEpoch)
		refreshDone <- errRefresh
	}()
	<-refreshStarted
	editDone := make(chan error, 1)
	go func() {
		_, errEdit := m.UpdateAndPersistMetadata(t.Context(), base.ID, func(a *Auth) error {
			a.Metadata["access_token"] = "replacement-access"
			a.Metadata["refresh_token"] = "replacement-refresh"
			a.Metadata["concurrency_limit"] = 3
			a.ProxyURL = "direct"
			a.Metadata["proxy_url"] = "direct"
			return nil
		})
		editDone <- errEdit
	}()
	<-saveGate.started
	releaseRefresh.Do(func() { close(refreshRelease) })
	// A no-op normalized to a terminal error must enter the same mutation gate
	// as a direct executor error before checking the current credential version.
	state := m.metadataPersistPreempt(base.ID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		state.mu.Lock()
		waiting := state.waiters > 0
		state.mu.Unlock()
		if waiting {
			break
		}
		select {
		case errRefresh := <-refreshDone:
			t.Fatalf("normalized refresh failure bypassed durable replacement: %v", errRefresh)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh did not wait for the mutation gate")
		}
		runtime.Gosched()
	}
	releaseSave.Do(func() { close(saveGate.release) })
	if errEdit := <-editDone; errEdit != nil {
		t.Fatal(errEdit)
	}
	if errRefresh := <-refreshDone; errRefresh == nil {
		t.Fatal("unchanged rejected refresh unexpectedly succeeded")
	}
	got, _ := m.GetByID(base.ID)
	if got.Metadata["access_token"] != "replacement-access" || got.Metadata["refresh_token"] != "replacement-refresh" || got.RequiresSignIn || got.RefreshError != nil || got.Unavailable || got.Status != StatusActive {
		t.Fatal("stale normalized refresh failure poisoned the replacement grant")
	}
	if got.RegistrationEpoch != base.RegistrationEpoch || got.CredentialVersion <= base.CredentialVersion || got.Metadata["codex_identity_namespace"] != "stable-v851" || got.ProxyURL != "direct" {
		t.Fatal("durable replacement lost identity, proxy or credential version fence")
	}
	persisted, errList := store.List(t.Context())
	if errList != nil || len(persisted) != 1 || persisted[0].Metadata["refresh_token"] != "replacement-refresh" {
		t.Fatal("runtime replacement differs from durable grant")
	}
	requireCredentialConcurrencySnapshot(t, m, base.ID, 0, 3)
}

func TestV851RemovingLocalRefreshGrantUnschedulesWithoutLosingControls(t *testing.T) {
	m, _, base := newMetadataControlsFixture(t, nil)
	m.RegisterExecutor(&countingRefreshExecutor{id: "codex"})
	base.Metadata["refresh_token"] = "local-refresh"
	base.Metadata["expired"] = "2000-01-01T00:00:00Z"
	base.Metadata["codex_identity_namespace"] = "stable-v851"
	base.Metadata["concurrency_limit"] = 3
	base.ProxyURL = "direct"
	base.Metadata["proxy_url"] = "direct"
	var err error
	base, err = m.Update(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	loop := newAuthAutoRefreshLoop(m, time.Second, 1)
	loop.upsert(base.ID, now)
	got, err := m.UpdateAndPersistMetadata(t.Context(), base.ID, func(a *Auth) error {
		delete(a.Metadata, "refresh_token")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loop.queueReschedule(base.ID)
	loop.applyDirty(now)
	if _, queued := loop.peek(); queued || m.shouldRefresh(got, now) {
		t.Fatal("access-only Codex credential retained automatic renewal work")
	}
	if got.RegistrationEpoch != base.RegistrationEpoch || got.Metadata["codex_identity_namespace"] != "stable-v851" || got.ProxyURL != "direct" {
		t.Fatal("removing renewal grant changed identity or proxy")
	}
	requireCredentialConcurrencySnapshot(t, m, base.ID, 0, 3)
}
