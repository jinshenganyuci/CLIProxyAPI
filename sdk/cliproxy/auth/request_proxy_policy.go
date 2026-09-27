package auth

import (
	"context"
	"errors"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

func isExecutionProxyPolicyError(err error) bool {
	var policyErr *proxyutil.ExecutionProxyError
	return errors.As(err, &policyErr)
}

// validateRequestProxyEntry handles only policies known before selection. An
// unpinned plugin-selected credential is checked once its identity is known.
func (m *Manager) validateRequestProxyEntry(ctx context.Context, opts cliproxyexecutor.Options) error {
	if _, err := proxyutil.ResolveExecutionProxy(cliproxyexecutor.RequestProxyURL(ctx), "", ""); err != nil {
		return err
	}
	if id := pinnedAuthIDFromMetadata(opts.Metadata); id != "" {
		if auth, ok := m.GetByID(id); ok {
			return m.validateSelectedRequestProxy(ctx, auth)
		}
	}
	return nil
}

func (m *Manager) validateSelectedRequestProxy(ctx context.Context, auth *Auth) error {
	requestProxy := cliproxyexecutor.RequestProxyURL(ctx)
	if requestProxy == "" || auth == nil {
		return nil
	}
	credentialProxy := auth.ProxyURL
	// Selection and auth preparation may observe different snapshots. Current
	// configuration remains authoritative without holding a lock during I/O.
	if m != nil && !m.HomeEnabled() {
		m.mu.RLock()
		if current := m.auths[auth.ID]; current != nil && current.RegistrationEpoch == auth.RegistrationEpoch {
			credentialProxy = current.ProxyURL
		}
		m.mu.RUnlock()
	}
	_, err := proxyutil.ResolveExecutionProxy(requestProxy, credentialProxy, "")
	return err
}
