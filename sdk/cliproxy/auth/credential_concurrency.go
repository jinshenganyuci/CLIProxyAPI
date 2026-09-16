package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialconcurrency"
)

// EffectiveConcurrencyLimit returns the local credential limit shared by all keys and models.
// Missing or invalid legacy values use the safe default; mutation boundaries reject invalid values.
func EffectiveConcurrencyLimit(auth *Auth) int {
	if auth != nil {
		if value, exists := auth.Metadata[credentialconcurrency.Field]; exists {
			if value == nil {
				return credentialconcurrency.DefaultLimit
			}
			if limit, errParse := credentialconcurrency.ParseValue(value); errParse == nil {
				return limit
			}
			return credentialconcurrency.DefaultLimit
		}
		if value := strings.TrimSpace(auth.Attributes[credentialconcurrency.Field]); value != "" {
			if limit, errParse := credentialconcurrency.ParseString(value); errParse == nil {
				return limit
			}
		}
	}
	return credentialconcurrency.DefaultLimit
}

// ValidateAuthConcurrencyLimit validates explicit limits at SDK mutation boundaries.
func ValidateAuthConcurrencyLimit(auth *Auth) error {
	if auth == nil {
		return nil
	}
	if value, exists := auth.Metadata[credentialconcurrency.Field]; exists && value != nil {
		if _, errParse := credentialconcurrency.ParseValue(value); errParse != nil {
			return errParse
		}
	}
	if value, exists := auth.Attributes[credentialconcurrency.Field]; exists {
		if _, errParse := credentialconcurrency.ParseString(value); errParse != nil {
			return errParse
		}
	}
	return nil
}

// CredentialConcurrencySnapshot observes a single instant without persisting runtime counters.
// Home owns admission when enabled, so callers must display Home-managed state separately.
func (m *Manager) CredentialConcurrencySnapshot(authID string) (inflight, limit int) {
	if m == nil {
		return 0, credentialconcurrency.DefaultLimit
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	limit = EffectiveConcurrencyLimit(m.auths[authID])
	m.credentialConcurrencyMu.Lock()
	inflight = m.credentialConcurrency[authID]
	m.credentialConcurrencyMu.Unlock()
	return inflight, limit
}

type credentialConcurrencyLease struct {
	once    sync.Once
	release func()
	cancel  context.CancelFunc
}

type credentialConcurrencyLeaseContextKey struct{}

type credentialConcurrencyAdmissionKey struct{}

func withCredentialConcurrencyAdmission(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, credentialConcurrencyAdmissionKey{}, true)
}

// filterCredentialConcurrencyCandidatesLocked is a preflight optimization, not an admission.
// Plugin scheduling can charge RPM, so known-full candidates must not require another plugin pick.
// The caller holds m.mu; the atomic lease acquisition still rechecks the limit after selection.
func (m *Manager) filterCredentialConcurrencyCandidatesLocked(ctx context.Context, candidates []*Auth) ([]*Auth, bool) {
	if ctx == nil || ctx.Value(credentialConcurrencyAdmissionKey{}) != true {
		return candidates, false
	}
	m.credentialConcurrencyMu.Lock()
	defer m.credentialConcurrencyMu.Unlock()
	filtered := make([]*Auth, 0, len(candidates))
	for _, auth := range candidates {
		if auth != nil && m.credentialConcurrency[auth.ID] < EffectiveConcurrencyLimit(auth) {
			filtered = append(filtered, auth)
		}
	}
	return filtered, len(filtered) != len(candidates)
}

func credentialConcurrencySelectionError(err error, filteredBusy bool) error {
	if !filteredBusy {
		return err
	}
	// The old plugin ABI reports no-match and all-bound-candidates-busy identically.
	// Only translate no-candidate errors after this request actually removed full candidates;
	// preserve authorization, quota, transport, and all other plugin errors verbatim.
	status := statusCodeFromError(err)
	if status != 0 && status != http.StatusServiceUnavailable {
		return err
	}
	code := ""
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		code = authErr.Code
	} else {
		var coded interface{ ErrorCode() string }
		if errors.As(err, &coded) {
			code = coded.ErrorCode()
		}
	}
	if code == "auth_not_found" || code == "auth_unavailable" {
		return newCredentialConcurrencyError()
	}
	return err
}

func (m *Manager) availableAuthsForConcurrencySelectorLocked(ctx context.Context, selector Selector, candidates []*Auth, provider, model string) ([]*Auth, []*Auth, bool, error) {
	if ctx == nil || ctx.Value(credentialConcurrencyAdmissionKey{}) != true {
		available, selectorAuths, errAvailable := m.availableAuthsForSelector(selector, candidates, provider, model, time.Now())
		return available, selectorAuths, false, errAvailable
	}
	// Check capacity before choosing the highest priority tier so a full tier cannot
	// hide an otherwise eligible lower-priority credential from the plugin/native selector.
	available, errAvailable := m.availableAuthsForRouteModelAcrossPriorities(candidates, provider, model, time.Now())
	if errAvailable != nil {
		return nil, nil, false, errAvailable
	}
	available, filteredBusy := m.filterCredentialConcurrencyCandidatesLocked(ctx, available)
	available = cloneAuthSlice(available)
	highest := highestPriorityAuths(available)
	if _, sessionAffinity := selector.(*SessionAffinitySelector); sessionAffinity {
		return highest, available, filteredBusy, nil
	}
	return highest, highest, filteredBusy, nil
}

func (lease *credentialConcurrencyLease) Release() {
	if lease != nil {
		if lease.cancel != nil {
			lease.cancel()
		}
		lease.releaseSlot()
	}
}

func (lease *credentialConcurrencyLease) releaseSlot() {
	if lease != nil {
		lease.once.Do(lease.release)
	}
}

func newCredentialConcurrencyError() error {
	return &Error{
		Code:       "credential_concurrency_exceeded",
		Message:    "all eligible credentials are at their concurrency limit",
		HTTPStatus: http.StatusTooManyRequests,
	}
}

// IsCredentialConcurrencyError identifies local admission rejection without upstream cooldown.
func IsCredentialConcurrencyError(err error) bool {
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && authErr.Code == "credential_concurrency_exceeded"
}

// AcquireCredentialConcurrency admits special credential execution paths into the same budget.
// The returned release function is idempotent and must run when execution completes or is canceled.
func (m *Manager) AcquireCredentialConcurrency(ctx context.Context, auth *Auth) (func(), error) {
	lease, errAcquire := m.acquireCredentialConcurrency(ctx, auth)
	if errAcquire != nil {
		return nil, errAcquire
	}
	return lease.Release, nil
}

// acquireCredentialConcurrency rechecks the current limit under the update lock before admission.
// Lock order is always m.mu then credentialConcurrencyMu. Counts survive deletion/re-registration
// of the same ID until every old request releases its lease, preventing a fresh capacity window.
func (m *Manager) acquireCredentialConcurrency(ctx context.Context, auth *Auth) (*credentialConcurrencyLease, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if m.HomeEnabled() {
		return nil, nil
	}
	if auth == nil || auth.ID == "" {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	m.mu.RLock()
	current := m.auths[auth.ID]
	if current == nil || current.Disabled || current.Status == StatusDisabled || current.RegistrationEpoch != auth.RegistrationEpoch {
		m.mu.RUnlock()
		return nil, &Error{Code: "auth_not_found", Message: "selected auth is no longer available"}
	}
	limit := EffectiveConcurrencyLimit(current)
	m.credentialConcurrencyMu.Lock()
	if m.credentialConcurrency[auth.ID] >= limit {
		m.credentialConcurrencyMu.Unlock()
		m.mu.RUnlock()
		return nil, newCredentialConcurrencyError()
	}
	if m.credentialConcurrency == nil {
		m.credentialConcurrency = make(map[string]int)
	}
	m.credentialConcurrency[auth.ID]++
	m.credentialConcurrencyMu.Unlock()
	m.mu.RUnlock()
	return &credentialConcurrencyLease{release: func() {
		m.credentialConcurrencyMu.Lock()
		if m.credentialConcurrency[auth.ID] <= 1 {
			delete(m.credentialConcurrency, auth.ID)
		} else {
			m.credentialConcurrency[auth.ID]--
		}
		m.credentialConcurrencyMu.Unlock()
	}}, nil
}
