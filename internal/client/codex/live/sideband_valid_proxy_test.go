package live

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	liveSidebandProxyTarget   = "sideband-valid-proxy.invalid:80"
	liveSidebandProxyUser     = "synthetic-user"
	liveSidebandProxyPassword = "synthetic-password"
)

func TestProxyAwareSidebandDialerValidCredentialRoutes(t *testing.T) {
	for _, route := range []string{"http", "socks5", "socks5h", "direct", "none"} {
		for _, global := range []string{"canary", "invalid"} {
			t.Run(route+"/"+global+"_global", func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				var globalHits, upstreamHits atomic.Int32
				canary := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					globalHits.Add(1)
					http.Error(writer, "unexpected global proxy", http.StatusBadGateway)
				}))
				t.Cleanup(canary.Close)

				const requestPath = "/v1/realtime?call_id=synthetic-call"
				const authorization = "Bearer synthetic-api-key"
				upstreamDone := make(chan error, 1)
				upgrader := websocket.Upgrader{}
				upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					upstreamDone <- func() error {
						upstreamHits.Add(1)
						if request.URL.RequestURI() != requestPath || request.Header.Get("Authorization") != authorization {
							http.Error(writer, "unexpected request", http.StatusBadRequest)
							return fmt.Errorf("upstream received unexpected request path or API key authorization")
						}
						if request.Header.Get("Proxy-Authorization") != "" {
							http.Error(writer, "unexpected proxy authorization", http.StatusBadRequest)
							return fmt.Errorf("proxy authorization leaked to the upstream")
						}
						conn, errUpgrade := upgrader.Upgrade(writer, request, nil)
						if errUpgrade != nil {
							return fmt.Errorf("upgrade local websocket: %w", errUpgrade)
						}
						defer func() { _ = conn.Close() }()
						stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
						defer stopClose()
						messageType, payload, errRead := conn.ReadMessage()
						if errRead != nil {
							return fmt.Errorf("read local websocket message: %w", errRead)
						}
						if errWrite := conn.WriteMessage(messageType, payload); errWrite != nil {
							return fmt.Errorf("echo local websocket message: %w", errWrite)
						}
						return nil
					}()
				}))
				t.Cleanup(upstream.Close)

				proxyURL := route
				wsURL := "ws" + strings.TrimPrefix(upstream.URL, "http") + requestPath
				var proxyHits *atomic.Int32
				var proxyDone <-chan error
				if route != "direct" && route != "none" {
					proxyURL, proxyHits, proxyDone = newLiveSidebandValidProxy(t, ctx, route, upstream.Listener.Addr().String())
					wsURL = "ws://" + liveSidebandProxyTarget + requestPath
				}
				cfg := &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: config.CodexCredentialProxyPolicyRequire}}
				cfg.ProxyURL = canary.URL
				if global == "invalid" {
					cfg.ProxyURL = "://invalid-global-proxy"
				}
				selected := &auth.Auth{
					ID:         "synthetic-api-key-auth",
					Provider:   "codex",
					ProxyURL:   proxyURL,
					Attributes: map[string]string{"api_key": "synthetic-api-key"},
				}
				if selected.AuthKind() != auth.AuthKindAPIKey {
					t.Fatal("fixture must select API key authentication")
				}
				if errPolicy := validateLiveCredentialProxyPolicy(cfg, selected); errPolicy != nil {
					t.Fatalf("API key credential proxy policy: %v", errPolicy)
				}
				dialer := newProxyAwareSidebandDialer(cfg, selected)
				conn, response, errDial := dialer.DialContext(ctx, wsURL, http.Header{"Authorization": {authorization}})
				if errDial != nil {
					t.Fatalf("dial selected %s route: %v", route, errDial)
				}
				defer func() { _ = conn.Close() }()
				stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stopClose()
				if response == nil || response.StatusCode != http.StatusSwitchingProtocols {
					t.Fatal("websocket did not return HTTP 101")
				}
				const payload = `{"type":"synthetic-sideband-echo"}`
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
					t.Fatalf("write websocket message: %v", errWrite)
				}
				messageType, echoed, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatalf("read websocket echo: %v", errRead)
				}
				if messageType != websocket.TextMessage || string(echoed) != payload {
					t.Fatal("websocket echo changed the message type or payload")
				}
				select {
				case errUpstream := <-upstreamDone:
					if errUpstream != nil {
						t.Fatalf("upstream fixture: %v", errUpstream)
					}
				case <-ctx.Done():
					t.Fatal("local websocket handler did not finish")
				}
				if proxyDone != nil {
					select {
					case errProxy := <-proxyDone:
						if errProxy != nil {
							t.Fatalf("proxy fixture: %v", errProxy)
						}
					case <-ctx.Done():
						t.Fatal("local proxy did not finish")
					}
					if got := proxyHits.Load(); got != 1 {
						t.Fatalf("credential proxy hits = %d, want 1", got)
					}
				}
				if got := upstreamHits.Load(); got != 1 {
					t.Fatalf("upstream hits = %d, want 1", got)
				}
				if got := globalHits.Load(); got != 0 {
					t.Fatalf("global proxy hits = %d, want 0", got)
				}
			})
		}
	}
}

func newLiveSidebandValidProxy(t *testing.T, ctx context.Context, scheme, upstreamAddress string) (string, *atomic.Int32, <-chan error) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen for local proxy: %v", errListen)
	}
	t.Cleanup(func() { _ = listener.Close() })
	hits := &atomic.Int32{}
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			client, errAccept := listener.Accept()
			if errAccept != nil {
				return fmt.Errorf("accept proxy connection: %w", errAccept)
			}
			defer func() { _ = client.Close() }()
			stopClient := context.AfterFunc(ctx, func() { _ = client.Close() })
			defer stopClient()
			hits.Add(1)
			buffered := bufio.NewReader(client)
			if errHandshake := readLiveSidebandProxyHandshake(client, buffered, scheme); errHandshake != nil {
				return errHandshake
			}
			remote, errDial := (&net.Dialer{}).DialContext(ctx, "tcp", upstreamAddress)
			if errDial != nil {
				return fmt.Errorf("dial local proxy upstream: %w", errDial)
			}
			defer func() { _ = remote.Close() }()
			stopRemote := context.AfterFunc(ctx, func() { _ = remote.Close() })
			defer stopRemote()
			response := []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80}
			if scheme == "http" {
				response = []byte("HTTP/1.1 200 Connection Established\r\n\r\n")
			}
			if _, errWrite := client.Write(response); errWrite != nil {
				return fmt.Errorf("write proxy connect response: %w", errWrite)
			}
			forwardDone := make(chan struct{})
			go func() {
				_, _ = io.Copy(remote, buffered)
				_ = remote.Close()
				close(forwardDone)
			}()
			_, _ = io.Copy(client, remote)
			_ = client.Close()
			<-forwardDone
			return nil
		}()
	}()
	proxyURL := &url.URL{Scheme: scheme, Host: listener.Addr().String(), User: url.UserPassword(liveSidebandProxyUser, liveSidebandProxyPassword)}
	return proxyURL.String(), hits, done
}

func readLiveSidebandProxyHandshake(client net.Conn, buffered *bufio.Reader, scheme string) error {
	if scheme == "http" {
		request, errRead := http.ReadRequest(buffered)
		if errRead != nil {
			return fmt.Errorf("read HTTP CONNECT: %w", errRead)
		}
		defer func() { _ = request.Body.Close() }()
		if request.Method != http.MethodConnect || request.Host != liveSidebandProxyTarget {
			return fmt.Errorf("unexpected HTTP CONNECT method or target")
		}
		wantAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte(liveSidebandProxyUser+":"+liveSidebandProxyPassword))
		if request.Header.Get("Proxy-Authorization") != wantAuthorization {
			return fmt.Errorf("HTTP CONNECT proxy authentication mismatch")
		}
		return nil
	}

	var header [2]byte
	if _, errRead := io.ReadFull(buffered, header[:]); errRead != nil {
		return fmt.Errorf("read SOCKS greeting: %w", errRead)
	}
	methods := make([]byte, int(header[1]))
	if _, errRead := io.ReadFull(buffered, methods); errRead != nil {
		return fmt.Errorf("read SOCKS authentication methods: %w", errRead)
	}
	if header[0] != 5 || !bytes.Contains(methods, []byte{2}) {
		return fmt.Errorf("SOCKS client did not offer username/password authentication")
	}
	if _, errWrite := client.Write([]byte{5, 2}); errWrite != nil {
		return fmt.Errorf("select SOCKS authentication: %w", errWrite)
	}
	if _, errRead := io.ReadFull(buffered, header[:]); errRead != nil {
		return fmt.Errorf("read SOCKS authentication header: %w", errRead)
	}
	username := make([]byte, int(header[1]))
	if _, errRead := io.ReadFull(buffered, username); errRead != nil {
		return fmt.Errorf("read SOCKS username: %w", errRead)
	}
	passwordLength, errLength := buffered.ReadByte()
	if errLength != nil {
		return fmt.Errorf("read SOCKS password length: %w", errLength)
	}
	password := make([]byte, int(passwordLength))
	if _, errRead := io.ReadFull(buffered, password); errRead != nil {
		return fmt.Errorf("read SOCKS password: %w", errRead)
	}
	if header[0] != 1 || string(username) != liveSidebandProxyUser || string(password) != liveSidebandProxyPassword {
		return fmt.Errorf("SOCKS username/password authentication mismatch")
	}
	if _, errWrite := client.Write([]byte{1, 0}); errWrite != nil {
		return fmt.Errorf("write SOCKS authentication response: %w", errWrite)
	}
	var connect [5]byte
	if _, errRead := io.ReadFull(buffered, connect[:]); errRead != nil {
		return fmt.Errorf("read SOCKS CONNECT header: %w", errRead)
	}
	if !bytes.Equal(connect[:4], []byte{5, 1, 0, 3}) {
		return fmt.Errorf("SOCKS CONNECT did not preserve the target hostname")
	}
	hostAndPort := make([]byte, int(connect[4])+2)
	if _, errRead := io.ReadFull(buffered, hostAndPort); errRead != nil {
		return fmt.Errorf("read SOCKS CONNECT target: %w", errRead)
	}
	portOffset := len(hostAndPort) - 2
	target := net.JoinHostPort(string(hostAndPort[:portOffset]), fmt.Sprint(binary.BigEndian.Uint16(hostAndPort[portOffset:])))
	if target != liveSidebandProxyTarget {
		return fmt.Errorf("unexpected SOCKS CONNECT target")
	}
	return nil
}
