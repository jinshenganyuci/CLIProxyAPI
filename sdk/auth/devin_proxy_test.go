package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type devinLoginProxyFixture struct {
	proxy *httptest.Server
	mu    sync.Mutex
	paths []string
}

func newDevinLoginProxyFixture(t *testing.T) *devinLoginProxyFixture {
	t.Helper()
	fixture := &devinLoginProxyFixture{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		fixture.paths = append(fixture.paths, request.URL.Path)
		fixture.mu.Unlock()
		switch request.URL.Path {
		case "/auth/cli/token":
			_, _ = io.WriteString(w, `{"token":"eyJmock.session.token"}`)
		case "/v3/self":
			_, _ = io.WriteString(w, `{"user_name":"cli-test","user_id":"cli-test-id"}`)
		case devinauth.DevinGetUserStatusPath:
			_, _ = w.Write([]byte{0x0a, 0x00})
		default:
			t.Errorf("unexpected OAuth path: %s", request.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	fixture.proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			t.Errorf("expected CONNECT, got %s", request.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		conn, errDial := net.Dial("tcp", upstream.Listener.Addr().String())
		if errDial != nil {
			t.Errorf("connect local mock upstream: %v", errDial)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		client, buffered, errHijack := w.(http.Hijacker).Hijack()
		if errHijack != nil {
			_ = conn.Close()
			t.Errorf("hijack proxy connection: %v", errHijack)
			return
		}
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		go func() {
			_, _ = io.Copy(conn, buffered)
			_ = conn.Close()
		}()
		_, _ = io.Copy(client, conn)
		_ = client.Close()
	}))
	t.Cleanup(fixture.proxy.Close)

	// Keep the production TLS and CONNECT paths, trusting only this local fixture.
	// ServerName selects the DNS name included in httptest's certificate.
	originalTransport := http.DefaultTransport
	transport := originalTransport.(*http.Transport).Clone()
	pool := x509.NewCertPool()
	pool.AddCert(upstream.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
		transport.CloseIdleConnections()
	})
	return fixture
}

func TestDevinAuthenticatorProxyPersistsAcrossConfigChange(t *testing.T) {
	fixture := newDevinLoginProxyFixture(t)
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fixture.proxy.URL}}
	opts := &LoginOptions{
		NoBrowser: true,
		Prompt: func(string) (string, error) {
			cfg.ProxyURL = "http://127.0.0.1:1"
			return "test-authorization-code", nil
		},
	}
	auth, errLogin := NewDevinAuthenticator().Login(context.Background(), cfg, opts)
	if errLogin != nil {
		t.Fatal(errLogin)
	}
	if auth.ProxyURL != fixture.proxy.URL || auth.Metadata["proxy_url"] != fixture.proxy.URL {
		t.Fatal("CLI credential lost the route selected before browser authentication")
	}
	if auth.Storage == nil {
		t.Fatal("CLI credential lacks proxy-preserving token storage")
	}
	fixture.mu.Lock()
	paths := append([]string(nil), fixture.paths...)
	fixture.mu.Unlock()
	if strings.Join(paths, ",") != "/auth/cli/token,/v3/self,"+devinauth.DevinGetUserStatusPath {
		t.Fatalf("not all CLI acquisition phases used the frozen proxy: %v", paths)
	}
}

func TestDevinAuthenticatorInvalidProxyFailsBeforeBrowser(t *testing.T) {
	for _, test := range []struct {
		name, global string
		metadata     map[string]string
	}{
		{name: "global", global: "invalid://user:secret@proxy.example"},
		{name: "explicit", global: "direct", metadata: map[string]string{"proxy_url": "invalid://user:secret@proxy.example"}},
		{name: "empty", metadata: map[string]string{"proxy_url": ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: test.global}}
			auth, errLogin := NewDevinAuthenticator().Login(context.Background(), cfg, &LoginOptions{Metadata: test.metadata})
			if errLogin == nil || auth != nil || strings.Contains(errLogin.Error(), "secret") {
				t.Fatal("invalid login proxy did not fail safely before browser authentication")
			}
		})
	}
}
