package helps

import (
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A separate process keeps the synthetic trust roots and environment proxy
// canaries out of the other transport tests and the process-wide root cache.
func TestUtlsALPNKeepsCredentialProxyRouting(t *testing.T) {
	if os.Getenv("CPA_TEST_UTLS_PROXY_ALPN_CHILD") != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestUtlsALPNKeepsCredentialProxyRouting$", "-test.v")
		command.Env = append(os.Environ(), "CPA_TEST_UTLS_PROXY_ALPN_CHILD=1", "GODEBUG="+os.Getenv("GODEBUG")+",x509usefallbackroots=1")
		output, errRun := command.CombinedOutput()
		if errRun != nil {
			t.Fatalf("isolated ALPN proxy tests: %v\n%s", errRun, output)
		}
		t.Logf("isolated ALPN proxy tests:\n%s", output)
		return
	}

	trustOrigin := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	roots := x509.NewCertPool()
	roots.AddCert(trustOrigin.Certificate())
	trustOrigin.Close()
	x509.SetFallbackRoots(roots)
	for _, protocol := range []string{"h2", "http/1.1", ""} {
		for _, route := range []string{"http", "socks5", "direct", "inherited-global"} {
			t.Run(protocol+"/"+route, func(t *testing.T) {
				var originHits, contextHits, environmentHits atomic.Int32
				origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					originHits.Add(1)
					wantVersion := 1
					if protocol == "h2" {
						wantVersion = 2
					}
					body, errRead := io.ReadAll(request.Body)
					if request.ProtoMajor != wantVersion || request.Method != http.MethodPost || request.URL.RequestURI() != "/selected?case=alpn" || request.Header.Get("X-Route-Test") != route || string(body) != "one request" || errRead != nil {
						t.Errorf("upstream request changed: protocol=%s method=%s uri=%s route=%q body=%q err=%v", request.Proto, request.Method, request.URL.RequestURI(), request.Header.Get("X-Route-Test"), body, errRead)
					}
					w.WriteHeader(http.StatusCreated)
					if _, errWrite := io.WriteString(w, "selected route"); errWrite != nil {
						t.Errorf("write origin response: %v", errWrite)
					}
				}))
				origin.EnableHTTP2 = protocol == "h2"
				origin.TLS = &stdtls.Config{NextProtos: []string{}}
				if protocol != "" {
					origin.TLS.NextProtos = []string{protocol}
				}
				origin.StartTLS()
				t.Cleanup(origin.Close)
				backend := strings.TrimPrefix(origin.URL, "https://")
				global := newGroundingProxyFixture(t, "http", backend)
				scheme := route
				if scheme != "socks5" {
					scheme = "http"
				}
				selected := newGroundingProxyFixture(t, scheme, backend)
				environment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					environmentHits.Add(1)
					http.Error(w, "environment proxy must not receive traffic", http.StatusBadGateway)
				}))
				t.Cleanup(environment.Close)
				for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
					t.Setenv(key, environment.URL)
				}
				for _, key := range []string{"NO_PROXY", "no_proxy"} {
					t.Setenv(key, "")
				}
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(*http.Request) (*http.Response, error) {
					contextHits.Add(1)
					return nil, fmt.Errorf("injected transport must not receive traffic")
				}))
				proxyURL := selected.URL()
				target := "https://example.com/selected?case=alpn"
				if route == "direct" {
					proxyURL = "direct"
					target = origin.URL + "/selected?case=alpn"
				} else if route == "inherited-global" {
					proxyURL = ""
				}
				client := NewUtlsHTTPClient(ctx, &config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL()}}, &cliproxyauth.Auth{ProxyURL: proxyURL}, 0)
				if client.Timeout != 0 {
					t.Fatal("inference transport introduced a request timeout")
				}
				// Select the configured Chrome transport directly so the standard
				// httptest certificate can be used without public DNS or host edits.
				client.Transport = client.Transport.(*fallbackRoundTripper).chrome
				request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader("one request"))
				if errRequest != nil {
					t.Fatal(errRequest)
				}
				request.Header.Set("X-Route-Test", route)
				response, errDo := client.Do(request)
				if errDo != nil {
					t.Fatal(errDo)
				}
				body, errRead := io.ReadAll(response.Body)
				if errClose := response.Body.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				if errRead != nil || response.StatusCode != http.StatusCreated || string(body) != "selected route" {
					t.Fatalf("response changed: status=%d body=%q err=%v", response.StatusCode, body, errRead)
				}
				wantSelected, wantGlobal := int32(1), int32(0)
				if route == "direct" {
					wantSelected = 0
				} else if route == "inherited-global" {
					wantSelected, wantGlobal = 0, 1
				}
				if originHits.Load() != 1 || selected.hits.Load() != wantSelected || global.hits.Load() != wantGlobal || contextHits.Load() != 0 || environmentHits.Load() != 0 {
					t.Fatalf("route counts: origin=%d selected=%d global=%d context=%d environment=%d", originHits.Load(), selected.hits.Load(), global.hits.Load(), contextHits.Load(), environmentHits.Load())
				}
				if wantSelected == 1 {
					selected.assertTargets(t, "example.com:443")
				} else if wantGlobal == 1 {
					global.assertTargets(t, "example.com:443")
				}
			})
		}
	}
}
