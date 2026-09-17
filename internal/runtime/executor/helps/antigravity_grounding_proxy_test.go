package helps

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

// These probes use actual proxy sockets and a local TLS origin. A direct dial or
// injected transport is a canary failure; no request reaches a public service.
func TestAntigravityGroundingHEADUsesOnlySelectedCredentialProxy(t *testing.T) {
	const redirectURL = "https://vertexaisearch.cloud.google.com/grounding-api-redirect/proxy-audit"
	const resolvedURL = "https://example.invalid/resolved"
	input := []byte(`{"candidates":[{"groundingMetadata":{"groundingChunks":[{"web":{"uri":"` + redirectURL + `"}},{"web":{"uri":"` + redirectURL + `"}}]}}]}`)
	var originHits, wrongOriginRequests, directDials, contextHits atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		originHits.Add(1)
		if request.Method != http.MethodHead || request.Host != "vertexaisearch.cloud.google.com" || request.URL.Path != "/grounding-api-redirect/proxy-audit" {
			wrongOriginRequests.Add(1)
		}
		w.Header().Set("Location", resolvedURL)
		w.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()
	backend := strings.TrimPrefix(origin.URL, "https://")
	httpProxy := newGroundingProxyFixture(t, "http", backend)
	socksProxy := newGroundingProxyFixture(t, "socks5", backend)
	globalProxy := newGroundingProxyFixture(t, "http", backend)

	originalTransport := http.DefaultTransport
	transport := originalTransport.(*http.Transport).Clone()
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: origin.Certificate().DNSNames[0]}
	transport.DisableKeepAlives = true
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != httpProxy.listener.Addr().String() && addr != globalProxy.listener.Addr().String() {
			directDials.Add(1)
			return nil, fmt.Errorf("unexpected direct origin dial")
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
		transport.CloseIdleConnections()
	})
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
		contextHits.Add(1)
		return nil, fmt.Errorf("unexpected injected transport")
	}))
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: globalProxy.URL()}}
	for _, proxy := range []*groundingProxyFixture{httpProxy, socksProxy} {
		t.Run(proxy.scheme, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Provider: "antigravity", ProxyURL: proxy.URL()}
			before := originHits.Load()
			output := ResolveAntigravityGroundingURLs(ctx, cfg, auth, input)
			for _, path := range []string{"candidates.0.groundingMetadata.groundingChunks.0.web.uri", "candidates.0.groundingMetadata.groundingChunks.1.web.uri"} {
				if got := gjson.GetBytes(output, path).String(); got != resolvedURL {
					t.Fatalf("resolved URL=%q, want %q", got, resolvedURL)
				}
			}
			if proxy.hits.Load() != 1 || originHits.Load() != before+1 {
				t.Fatalf("expected one proxy tunnel and one deduplicated HEAD: proxy=%d origin delta=%d", proxy.hits.Load(), originHits.Load()-before)
			}
			proxy.assertTargets(t, "vertexaisearch.cloud.google.com:443")
		})
	}
	for _, malformed := range []string{"bad-proxy", "ftp://example.invalid:1080", "http://user:secret@%zz"} {
		t.Run("invalid/"+malformed, func(t *testing.T) {
			before := originHits.Load()
			output := ResolveAntigravityGroundingURLs(ctx, cfg, &cliproxyauth.Auth{Provider: "antigravity", ProxyURL: malformed}, input)
			if string(output) != string(input) || originHits.Load() != before {
				t.Fatal("invalid proxy unexpectedly resolved grounding URLs")
			}
		})
	}
	if directDials.Load() != 0 || contextHits.Load() != 0 || globalProxy.hits.Load() != 0 || wrongOriginRequests.Load() != 0 {
		t.Fatalf("route violation: direct=%d context=%d global=%d wrong origin=%d", directDials.Load(), contextHits.Load(), globalProxy.hits.Load(), wrongOriginRequests.Load())
	}
}

type groundingProxyFixture struct {
	scheme   string
	listener net.Listener
	hits     atomic.Int32
	mu       sync.Mutex
	targets  []string
	conns    map[net.Conn]bool
	wg       sync.WaitGroup
}

func (proxy *groundingProxyFixture) URL() string {
	return proxy.scheme + "://" + proxy.listener.Addr().String()
}

func (proxy *groundingProxyFixture) assertTargets(t *testing.T, expected string) {
	t.Helper()
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if len(proxy.targets) != 1 || proxy.targets[0] != expected {
		t.Fatalf("proxy targets=%v, want [%s]", proxy.targets, expected)
	}
}

func newGroundingProxyFixture(t *testing.T, scheme, backend string) *groundingProxyFixture {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	proxy := &groundingProxyFixture{scheme: scheme, listener: listener, conns: make(map[net.Conn]bool)}
	proxy.wg.Add(1)
	go func() {
		defer proxy.wg.Done()
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			proxy.mu.Lock()
			proxy.conns[conn] = true
			proxy.mu.Unlock()
			proxy.wg.Add(1)
			go func() {
				defer proxy.wg.Done()
				defer func() {
					_ = conn.Close()
					proxy.mu.Lock()
					delete(proxy.conns, conn)
					proxy.mu.Unlock()
				}()
				reader := bufio.NewReader(conn)
				target, errHandshake := proxy.handshake(conn, reader)
				if errHandshake != nil {
					return
				}
				proxy.hits.Add(1)
				proxy.mu.Lock()
				proxy.targets = append(proxy.targets, target)
				proxy.mu.Unlock()
				upstream, errDial := net.Dial("tcp", backend)
				if errDial != nil {
					return
				}
				defer func() { _ = upstream.Close() }()
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(upstream, reader)
					_ = upstream.Close()
					close(done)
				}()
				_, _ = io.Copy(conn, upstream)
				_ = conn.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		proxy.mu.Lock()
		for conn := range proxy.conns {
			_ = conn.Close()
		}
		proxy.mu.Unlock()
		proxy.wg.Wait()
	})
	return proxy
}

func (proxy *groundingProxyFixture) handshake(conn net.Conn, reader *bufio.Reader) (string, error) {
	if proxy.scheme == "http" {
		request, errRead := http.ReadRequest(reader)
		if errRead != nil {
			return "", errRead
		}
		if request.Method != http.MethodConnect {
			return "", fmt.Errorf("expected CONNECT")
		}
		_, errWrite := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		return request.Host, errWrite
	}
	var greeting [2]byte
	if _, errRead := io.ReadFull(reader, greeting[:]); errRead != nil {
		return "", errRead
	}
	methods := make([]byte, int(greeting[1]))
	if _, errRead := io.ReadFull(reader, methods); errRead != nil {
		return "", errRead
	}
	if _, errWrite := conn.Write([]byte{5, 0}); errWrite != nil {
		return "", errWrite
	}
	var command [5]byte
	if _, errRead := io.ReadFull(reader, command[:]); errRead != nil {
		return "", errRead
	}
	if command[0] != 5 || command[1] != 1 || command[3] != 3 {
		return "", fmt.Errorf("expected SOCKS5 CONNECT with proxy-side DNS")
	}
	host := make([]byte, int(command[4]))
	if _, errRead := io.ReadFull(reader, host); errRead != nil {
		return "", errRead
	}
	var port [2]byte
	if _, errRead := io.ReadFull(reader, port[:]); errRead != nil {
		return "", errRead
	}
	_, errWrite := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
	return net.JoinHostPort(string(host), fmt.Sprint(binary.BigEndian.Uint16(port[:]))), errWrite
}
