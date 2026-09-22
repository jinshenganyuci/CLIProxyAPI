package helps

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

type devinTransportKey struct {
	proxyURL string
	base     *http.Transport
}

var devinTransportCache = NewTransportCache[devinTransportKey](DefaultTransportCacheCapacity)

// NewDevinHTTPClient preserves Devin Connect-RPC connection reuse and disables
// automatic gzip. A selected credential or global route takes precedence over
// injected transports, and an invalid selected route never falls back to direct.
func NewDevinHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) (*http.Client, error) {
	proxyURL, errResolve := ResolveExecutionProxy(ctx, cfg, auth)
	if errResolve != nil {
		return nil, errResolve
	}

	if proxyURL != "" {
		tr, errTransport := devinTransportCache.Get(devinTransportKey{proxyURL: proxyURL}, func() (*http.Transport, error) {
			base, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
			if errBuild != nil {
				return nil, errBuild
			}
			base.DisableCompression = true
			return base, nil
		})
		if errTransport != nil {
			return nil, fmt.Errorf("devin proxy transport: %w", errTransport)
		}
		return &http.Client{Transport: tr, Timeout: timeout}, nil
	}

	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			if base, ok := rt.(*http.Transport); ok && base != nil {
				return newDevinClientWithTransport(base, timeout)
			}
			return &http.Client{Transport: devinNoGzipRoundTripper{base: rt}, Timeout: timeout}, nil
		}
	}

	base, _ := http.DefaultTransport.(*http.Transport)
	return newDevinClientWithTransport(base, timeout)
}

func newDevinClientWithTransport(base *http.Transport, timeout time.Duration) (*http.Client, error) {
	tr, errTransport := devinTransportCache.Get(devinTransportKey{base: base}, func() (*http.Transport, error) {
		cloned := &http.Transport{}
		if base != nil {
			cloned = base.Clone()
		}
		cloned.DisableCompression = true
		return cloned, nil
	})
	if errTransport != nil {
		return nil, fmt.Errorf("devin HTTP transport: %w", errTransport)
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

type devinNoGzipRoundTripper struct {
	base http.RoundTripper
}

func (rt devinNoGzipRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Accept-Encoding") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("Accept-Encoding", "identity")
	}
	return rt.base.RoundTrip(req)
}
