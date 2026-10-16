package helps

import (
	"context"
	"errors"
	"net/http"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// tlsHandshakeTimeoutContextKey carries the opt-in TLS establishment bound
// through request contexts. The Claude Code round tripper is cached per
// proxy URL, so the bound cannot live on the transport itself: it is read
// per dial instead, which also lets config hot reloads take effect
// without rebuilding cached transports.
type tlsHandshakeTimeoutContextKey struct{}

type tlsEstablishmentCallerContextKey struct{}

// withTLSEstablishmentCaller retains the request cancellation signal across
// net/http's detached dial context. Only connection acquisition uses this value;
// an established pooled connection must not retain its first caller's lifetime.
func withTLSEstablishmentCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, tlsEstablishmentCallerContextKey{}, ctx)
}

func tlsEstablishmentContext(ctx context.Context) (context.Context, context.CancelFunc) {
	caller, _ := ctx.Value(tlsEstablishmentCallerContextKey{}).(context.Context)
	if caller == nil {
		return ctx, func() {}
	}
	linked, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(caller, cancel)
	if caller.Err() != nil {
		cancel()
	}
	return linked, func() {
		stop()
		cancel()
	}
}

// withTLSHandshakeTimeout returns a context carrying the TLS establishment
// bound. A non-positive timeout leaves the context unchanged (bound
// disabled), matching the config contract where 0 means off.
func withTLSHandshakeTimeout(ctx context.Context, timeout time.Duration) context.Context {
	if ctx == nil || timeout <= 0 {
		return ctx
	}
	return context.WithValue(ctx, tlsHandshakeTimeoutContextKey{}, timeout)
}

// tlsHandshakeTimeoutFromContext returns the configured establishment
// bound, or 0 when the bound is disabled.
func tlsHandshakeTimeoutFromContext(ctx context.Context) time.Duration {
	if ctx == nil {
		return 0
	}
	timeout, _ := ctx.Value(tlsHandshakeTimeoutContextKey{}).(time.Duration)
	if timeout <= 0 {
		return 0
	}
	return timeout
}

// tlsHandshakeTimeoutRoundTripper stamps the configured establishment
// bound onto each outgoing request context so the uTLS dial paths can
// read it per dial. It only ever annotates the context; request routing,
// headers, and bodies pass through unchanged.
type tlsHandshakeTimeoutRoundTripper struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (rt *tlsHandshakeTimeoutRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.timeout > 0 {
		req = req.WithContext(withTLSHandshakeTimeout(req.Context(), rt.timeout))
	}
	return rt.base.RoundTrip(req)
}

// handshakeContext bounds the TLS handshake when a positive timeout is
// configured on ctx. It starts when the caller invokes it, after dialing
// and proxy CONNECT have returned. It returns the context to hand to
// HandshakeContext plus its cancel function. The bound exists only for
// the duration of the handshake call: crypto/tls (and uTLS) close the raw
// connection when the handshake context expires, and once the handshake
// succeeds the context plays no further role, so inference and
// long-running streams are never time-limited and no connection deadline
// is left behind. When the bound is disabled the original context is
// returned unchanged with a no-op cancel, preserving the previous
// behavior exactly.
func handshakeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := tlsHandshakeTimeoutFromContext(ctx)
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// classifyHandshakeError annotates only a deadline produced by the
// configured internal handshake bound. The parent context is checked so a
// caller cancellation or caller-owned deadline racing the internal timer
// retains its original error and non-retryable classification.
func classifyHandshakeError(parentCtx, handshakeCtx context.Context, err error) error {
	if err == nil || tlsHandshakeTimeoutFromContext(parentCtx) <= 0 {
		return err
	}
	if parentCtx != nil && parentCtx.Err() != nil {
		return err
	}
	if handshakeCtx == nil || !errors.Is(handshakeCtx.Err(), context.DeadlineExceeded) {
		return err
	}
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(err)
}
