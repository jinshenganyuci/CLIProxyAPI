package auth

import (
	"context"
	"errors"
	"net/http"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// SelectAuthByKindWithConcurrency selects and reserves one local credential for a new execution.
// Existing session controls should use SelectAuthByKind without taking another reservation.
func (m *Manager) SelectAuthByKindWithConcurrency(ctx context.Context, provider, model, requiredKind string, opts cliproxyexecutor.Options) (*Auth, func(), error) {
	requiredKind = normalizeAuthKind(requiredKind)
	if requiredKind == "" {
		return nil, nil, &Error{Code: "invalid_auth_kind", Message: "required auth kind is invalid", HTTPStatus: http.StatusBadRequest}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.selectAuthWithConcurrency(withRequiredAuthKind(ctx, requiredKind), provider, model, opts)
}

// SelectAuthWithCredentialPolicyAndConcurrency reserves a local credential allowed by a fixed policy.
func (m *Manager) SelectAuthWithCredentialPolicyAndConcurrency(ctx context.Context, provider, model, policy string, opts cliproxyexecutor.Options) (*Auth, func(), error) {
	policy = normalizeCredentialPolicy(policy)
	if policy == "" {
		return nil, nil, &Error{Code: "invalid_credential_policy", Message: "credential policy is invalid", HTTPStatus: http.StatusBadRequest}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.selectAuthWithConcurrency(withCredentialPolicy(ctx, policy), provider, model, opts)
}

func (m *Manager) selectAuthWithConcurrency(ctx context.Context, provider, model string, opts cliproxyexecutor.Options) (*Auth, func(), error) {
	if m == nil || m.HomeEnabled() {
		return nil, nil, &Error{Code: "home_unavailable", Message: "local execution selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	ctx = withCredentialConcurrencyAdmission(ctx)
	tried := make(map[string]struct{})
	var busyErr error
	for {
		if errContext := ctx.Err(); errContext != nil {
			return nil, nil, errContext
		}
		selected, _, errPick := m.pickNextLegacy(ctx, provider, model, opts, tried)
		if errPick != nil {
			var authErr *Error
			if busyErr != nil && errors.As(errPick, &authErr) && (authErr.Code == "auth_not_found" || authErr.Code == "auth_unavailable") {
				return nil, nil, busyErr
			}
			return nil, nil, errPick
		}
		if selected == nil {
			return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no eligible auth"}
		}
		if m.HomeEnabled() {
			return nil, nil, &Error{Code: "home_unavailable", Message: "local execution selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
		}
		release, errAcquire := m.AcquireCredentialConcurrency(ctx, selected)
		if errAcquire == nil {
			return selected, release, nil
		}
		if !IsCredentialConcurrencyError(errAcquire) || m.hasPluginScheduler() {
			return nil, nil, errAcquire
		}
		if _, repeated := tried[selected.ID]; repeated {
			return nil, nil, errAcquire
		}
		tried[selected.ID] = struct{}{}
		busyErr = errAcquire
	}
}
