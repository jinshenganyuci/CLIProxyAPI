package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCredentialConcurrencyRecoveryDeadlineSelectionKeepsLocalBusyContract(t *testing.T) {
	now := time.Unix(1800000000, 0)
	upstream := errors.New("synthetic temporary upstream failure")
	unavailable := newAuthUnavailableErrorWithCause(now.Add(time.Minute), now, upstream)

	for _, filteredBusy := range []bool{false, true} {
		t.Run(fmt.Sprintf("filtered-busy-%t", filteredBusy), func(t *testing.T) {
			err := credentialConcurrencySelectionError(fmt.Errorf("selection: %w", unavailable), filteredBusy)
			if !filteredBusy {
				if IsCredentialConcurrencyError(err) || statusCodeFromError(err) != http.StatusServiceUnavailable {
					t.Fatalf("temporary unavailability changed classification: %v", err)
				}
				if got := SafeResponseHeaders(err).Get("Retry-After"); got != "60" {
					t.Fatalf("temporary recovery hint = %q, want 60", got)
				}
				if !errors.Is(err, upstream) {
					t.Fatal("temporary upstream cause was lost")
				}
				return
			}

			if !IsCredentialConcurrencyError(err) || statusCodeFromError(err) != http.StatusTooManyRequests {
				t.Fatalf("local admission must remain typed 429: %v", err)
			}
			if got := SafeResponseHeaders(err).Get("Retry-After"); got != "" {
				t.Fatalf("local busy inherited unrelated cooldown hint: %q", got)
			}
			manager, _, _, _ := newCredentialConcurrencyManager(t, 1, 1)
			wait, retry := manager.shouldRetryAfterErrorWithAttempted(context.Background(), cliproxyexecutor.Options{}, err, 0, []string{"test"}, "model", time.Minute, -1, 999, nil)
			if retry || wait != 0 {
				t.Fatalf("local busy retried after deadline wrapping: wait=%v retry=%t", wait, retry)
			}
			if shouldAttemptAntigravityCreditsFallback(manager, err, []string{"antigravity"}) {
				t.Fatal("local busy activated paid credits after deadline wrapping")
			}
		})
	}
}

func TestCredentialConcurrencyFullAndTemporarilyBlockedAuthDoNotBecomeCooldown(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			manager, executor, auths, models := newCredentialConcurrencyManager(t, 2, 1)
			blocked := auths[1].Clone()
			blocked.Unavailable = true
			blocked.NextRetryAfter = time.Now().Add(time.Hour)
			blocked.LastError = &Error{Code: "temporary_unavailable", Message: "synthetic temporary failure", HTTPStatus: http.StatusServiceUnavailable}
			if _, errUpdate := manager.Update(context.Background(), blocked); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), auths[0])
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer release()
			executor.execute = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				t.Error("busy or temporarily blocked credential reached executor")
				return cliproxyexecutor.Response{}, nil
			}
			executor.stream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				t.Error("busy or temporarily blocked credential reached stream executor")
				return nil, nil
			}
			request := cliproxyexecutor.Request{Model: models[0], Payload: []byte(`{}`)}
			var err error
			switch mode {
			case "execute":
				_, err = manager.Execute(context.Background(), []string{"test"}, request, cliproxyexecutor.Options{})
			case "count":
				_, err = manager.ExecuteCount(context.Background(), []string{"test"}, request, cliproxyexecutor.Options{})
			case "stream":
				_, err = manager.ExecuteStream(context.Background(), []string{"test"}, request, cliproxyexecutor.Options{})
			}
			if !IsCredentialConcurrencyError(err) || statusCodeFromError(err) != http.StatusTooManyRequests {
				t.Fatalf("expected local capacity error, got %T %v", err, err)
			}
			if got := SafeResponseHeaders(err).Get("Retry-After"); got != "" {
				t.Fatalf("local capacity error inherited temporary deadline: %q", got)
			}
			current, _ := manager.GetByID(auths[0].ID)
			if current.Unavailable || current.LastError != nil || current.Quota.Exceeded || !current.NextRetryAfter.IsZero() {
				t.Fatalf("local busy changed healthy credential state: %#v", current)
			}
			requireCredentialConcurrencySnapshot(t, manager, auths[0].ID, 1, 1)
			release()
			requireCredentialConcurrencySnapshot(t, manager, auths[0].ID, 0, 1)
		})
	}
}
