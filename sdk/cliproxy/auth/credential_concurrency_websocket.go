package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (m *Manager) withWebsocketResponseAdmission(ctx context.Context, auth *Auth, initial *credentialConcurrencyLease, provider, model string, opts cliproxyexecutor.Options) context.Context {
	if initial == nil || cliproxyexecutor.WebsocketInputFromContext(ctx) == nil {
		return ctx
	}
	return cliproxyexecutor.WithWebsocketResponseAdmission(ctx, &cliproxyexecutor.WebsocketResponseAdmission{
		ReleaseInitial: initial.releaseSlot,
		Acquire: func(responseCtx context.Context) (func(), error) {
			if responseCtx == nil {
				responseCtx = ctx
			}
			if errCanceled := responseCtx.Err(); errCanceled != nil {
				return nil, errCanceled
			}
			m.mu.RLock()
			current := m.auths[auth.ID]
			scheduler := m.pluginScheduler
			if current == nil || !authSelectionEligibilityForRequest(ctx, opts).allows(current) || !m.authSupportsRouteModel(registry.GetGlobalRegistry(), current, model) {
				m.mu.RUnlock()
				return nil, &Error{Code: "auth_not_found", Message: "duplex credential is no longer eligible", HTTPStatus: http.StatusServiceUnavailable}
			}
			_, errAvailable := m.availableAuthsForRouteModel([]*Auth{current}, provider, model, time.Now())
			current = current.Clone()
			m.mu.RUnlock()
			if errAvailable != nil {
				return nil, errAvailable
			}
			lease, errAcquire := m.acquireCredentialConcurrency(responseCtx, auth)
			if errAcquire != nil {
				return nil, errAcquire
			}
			if lease == nil {
				return nil, &Error{Code: "home_unavailable", Message: "duplex connection requires renewed Home admission", HTTPStatus: http.StatusServiceUnavailable}
			}
			// Cancellation and the executor's terminal frame may race. The underlying
			// lease makes both paths idempotent without canceling the parent socket.
			stopCancellation := context.AfterFunc(responseCtx, lease.Release)
			release := func() {
				stopCancellation()
				lease.Release()
			}
			// Capacity is reserved before the scheduler can charge RPM. Restrict its
			// candidates to the authenticated socket's credential; never reroute a turn.
			if scheduler != nil {
				request := pluginapi.SchedulerPickRequest{
					Provider: provider, Providers: schedulerProviders(provider, nil), Model: model, Stream: true,
					Options: schedulerOptions(opts), Candidates: schedulerAuthCandidates([]*Auth{current}),
				}
				response, handled, errPick := scheduler.PickAuth(responseCtx, request)
				if errPick != nil {
					release()
					return nil, errPick
				}
				if handled && response.Handled {
					selected := strings.TrimSpace(response.AuthID)
					_, delegate := builtinSchedulerStrategy(response.DelegateBuiltin)
					if (selected != "" && selected != current.ID) || (selected == "" && !delegate) {
						release()
						return nil, &Error{Code: "auth_unavailable", Message: "duplex scheduler cannot change the bound credential", HTTPStatus: http.StatusForbidden}
					}
				}
			}
			if errCanceled := responseCtx.Err(); errCanceled != nil {
				release()
				return nil, errCanceled
			}
			// A plugin may be slow. Recheck the selected registration and routing
			// state after authorization without selecting or charging a second time.
			m.mu.RLock()
			latest := m.auths[auth.ID]
			if m.HomeEnabled() || latest == nil || latest.RegistrationEpoch != auth.RegistrationEpoch || !authSelectionEligibilityForRequest(ctx, opts).allows(latest) || !m.authSupportsRouteModel(registry.GetGlobalRegistry(), latest, model) {
				m.mu.RUnlock()
				release()
				return nil, &Error{Code: "auth_not_found", Message: "duplex credential changed during authorization", HTTPStatus: http.StatusServiceUnavailable}
			}
			_, errAvailable = m.availableAuthsForRouteModel([]*Auth{latest}, provider, model, time.Now())
			m.mu.RUnlock()
			if errAvailable != nil {
				release()
				return nil, errAvailable
			}
			return release, nil
		},
	})
}
