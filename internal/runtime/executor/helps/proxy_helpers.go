package helps

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Preserve an explicit credential route and reject a conflicting request override
// 2. Use a request override when the credential has no configured route
// 3. Use cfg.ProxyURL, then an injected RoundTripper when no route is configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	proxyURL, errResolve := ResolveExecutionProxy(ctx, cfg, auth)
	if errResolve != nil {
		httpClient.Transport = proxyConfigurationErrorTransport{err: errResolve}
		return httpClient
	}

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
		if errBuild != nil || transport == nil {
			httpClient.Transport = proxyConfigurationErrorTransport{err: fmt.Errorf("selected proxy transport could not be initialized")}
		} else {
			httpClient.Transport = transport
		}
		return httpClient
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			httpClient.Transport = rt
		}
	}

	return httpClient
}

// proxyConfigurationErrorTransport prevents an invalid selected route from
// silently sending a request through an unrelated transport or direct connection.
type proxyConfigurationErrorTransport struct {
	err error
}

func (t proxyConfigurationErrorTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request != nil && request.Body != nil {
		if errClose := request.Body.Close(); errClose != nil {
			log.Debug("failed to close request body after rejecting invalid proxy")
		}
	}
	return nil, t.err
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}

// ResolveExecutionProxy preserves credential egress while supporting unbound request routes.
func ResolveExecutionProxy(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (string, error) {
	credentialProxy, globalProxy := "", ""
	if auth != nil {
		credentialProxy = auth.ProxyURL
	}
	if cfg != nil {
		globalProxy = cfg.ProxyURL
	}
	return proxyutil.ResolveExecutionProxy(cliproxyexecutor.RequestProxyURL(ctx), credentialProxy, globalProxy)
}
