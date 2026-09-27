package xai

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// The constructor supplies every dialer and proxy decision. These tests add
// only trust for the local TLS origin and verify both ends of the proxy socket.
func TestXAIProxyUpgrade737Network(t *testing.T) {
	for _, tc := range []struct {
		name          string
		selected      string
		wantErr       bool
		invalidGlobal bool
	}{
		{name: "http_over_global", selected: "http"},
		{name: "authenticated_socks5_over_global", selected: "socks5"},
		{name: "authenticated_socks5h_over_global", selected: "socks5h"},
		{name: "direct_over_global", selected: "direct"},
		{name: "none_over_global", selected: "none"},
		{name: "empty_inherits_global", selected: ""},
		{name: "http_over_invalid_global", selected: "http", invalidGlobal: true},
		{name: "direct_over_invalid_global", selected: "direct", invalidGlobal: true},
		{name: "http_rejection_never_falls_back", selected: "reject", wantErr: true},
		{name: "unreachable_never_falls_back", selected: "unreachable", wantErr: true},
		{name: "socks_password_error_never_falls_back", selected: "bad_password", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var originMu sync.Mutex
			var originPeers []string
			var badForms int
			refreshToken := "synthetic-network-refresh-" + tc.name
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				errForm := r.ParseForm()
				originMu.Lock()
				originPeers = append(originPeers, r.RemoteAddr)
				if errForm != nil || r.Method != http.MethodPost || r.URL.Path != "/token" ||
					r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != refreshToken ||
					r.Form.Get("client_id") != ClientID {
					badForms++
				}
				originMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"synthetic-network-access","refresh_token":"synthetic-network-next","token_type":"Bearer","expires_in":3600}`)
			}))
			t.Cleanup(origin.Close)
			backend := origin.Listener.Addr().String()
			global := newXAIProxyUpgrade737Server(t, "http", backend, false)
			cfg := &config.Config{}
			cfg.ProxyURL = global.url("http", false)
			if tc.invalidGlobal {
				cfg.ProxyURL = "unsupported://invalid-global.example.invalid:1080"
			}
			var selected *xaiProxyUpgrade737Server
			proxyURL := tc.selected
			switch tc.selected {
			case "http", "reject":
				selected = newXAIProxyUpgrade737Server(t, "http", backend, tc.selected == "reject")
				proxyURL = selected.url("http", false)
			case "socks5", "socks5h", "bad_password":
				selected = newXAIProxyUpgrade737Server(t, "socks5", backend, false)
				scheme := tc.selected
				if scheme == "bad_password" {
					scheme = "socks5"
				}
				proxyURL = selected.url(scheme, tc.selected == "bad_password")
			case "unreachable":
				closed, errListen := net.Listen("tcp", "127.0.0.1:0")
				if errListen != nil {
					t.Fatal(errListen)
				}
				proxyURL = "http://" + closed.Addr().String()
				if errClose := closed.Close(); errClose != nil {
					t.Fatal(errClose)
				}
			}
			auth := NewXAIAuthWithProxyURL(cfg, proxyURL)
			transport, ok := auth.httpClient.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("expected constructor-created HTTP transport, got %T", auth.httpClient.Transport)
			}
			roots := x509.NewCertPool()
			roots.AddCert(origin.Certificate())
			transport.TLSClientConfig = &tls.Config{RootCAs: roots}
			t.Cleanup(auth.httpClient.CloseIdleConnections)
			token, errRefresh := auth.RefreshTokens(t.Context(), refreshToken, origin.URL+"/token")
			if tc.wantErr {
				if errRefresh == nil || token != nil {
					t.Fatalf("failed proxy returned token=%v error=%v", token != nil, errRefresh)
				}
			} else if errRefresh != nil || token == nil || token.AccessToken != "synthetic-network-access" || token.RefreshToken != "synthetic-network-next" {
				t.Fatalf("refresh failed: token=%v error=%v", token != nil, errRefresh)
			}
			originMu.Lock()
			peers := append([]string(nil), originPeers...)
			invalidForms := badForms
			originMu.Unlock()
			if invalidForms != 0 {
				t.Fatalf("invalid token exchange forms: %d", invalidForms)
			}
			globalSnapshot := global.snapshot()
			if tc.selected != "" && globalSnapshot.requests != 0 {
				t.Fatalf("credential proxy unexpectedly reached global proxy: %d", globalSnapshot.requests)
			}
			if tc.wantErr {
				if len(peers) != 0 {
					t.Fatalf("failed proxy reached origin through another route: %v", peers)
				}
				if selected != nil {
					snapshot := selected.snapshot()
					if snapshot.requests != 1 || snapshot.rejected != 1 || len(snapshot.sources) != 0 || len(snapshot.errors) != 0 {
						t.Fatalf("expected one observed proxy rejection: %+v", snapshot)
					}
				}
				t.Log("proxy failure observed; origin requests=0; global proxy requests=0")
				return
			}
			if len(peers) != 1 {
				t.Fatalf("origin requests=%d, want 1", len(peers))
			}
			if tc.selected == "" {
				selected = global
			}
			if selected != nil {
				snapshot := selected.snapshot()
				if snapshot.requests != 1 || snapshot.rejected != 0 || len(snapshot.targets) != 1 || snapshot.targets[0] != backend ||
					len(snapshot.sources) != 1 || snapshot.sources[0] != peers[0] || len(snapshot.errors) != 0 {
					t.Fatalf("proxy socket and origin evidence disagree: proxy=%+v origin=%v", snapshot, peers)
				}
				t.Logf("one token exchange; origin peer %s matches selected proxy outbound socket", peers[0])
			} else {
				t.Logf("one direct token exchange from %s; global proxy requests=0", peers[0])
			}
		})
	}
}

type xaiProxyUpgrade737Snapshot struct {
	requests int
	rejected int
	targets  []string
	sources  []string
	errors   []string
}

type xaiProxyUpgrade737Server struct {
	listener net.Listener
	scheme   string
	backend  string
	reject   bool
	mu       sync.Mutex
	data     xaiProxyUpgrade737Snapshot
	conns    map[net.Conn]struct{}
	accepted chan struct{}
	workers  sync.WaitGroup
}

func (p *xaiProxyUpgrade737Server) snapshot() xaiProxyUpgrade737Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return xaiProxyUpgrade737Snapshot{
		requests: p.data.requests,
		rejected: p.data.rejected,
		targets:  append([]string(nil), p.data.targets...),
		sources:  append([]string(nil), p.data.sources...),
		errors:   append([]string(nil), p.data.errors...),
	}
}

func (p *xaiProxyUpgrade737Server) url(scheme string, badPassword bool) string {
	credentials := ""
	if strings.HasPrefix(scheme, "socks5") {
		credentials = "fixture-user:fixture-password@"
		if badPassword {
			credentials = "fixture-user:incorrect-password@"
		}
	}
	return scheme + "://" + credentials + p.listener.Addr().String()
}

func newXAIProxyUpgrade737Server(t *testing.T, scheme, backend string, reject bool) *xaiProxyUpgrade737Server {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	p := &xaiProxyUpgrade737Server{listener: listener, scheme: scheme, backend: backend, reject: reject,
		conns: make(map[net.Conn]struct{}), accepted: make(chan struct{})}
	go func() {
		defer close(p.accepted)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			p.mu.Lock()
			p.conns[conn] = struct{}{}
			p.data.requests++
			p.mu.Unlock()
			p.workers.Add(1)
			go func() {
				defer p.workers.Done()
				defer func() {
					_ = conn.Close()
					p.mu.Lock()
					delete(p.conns, conn)
					p.mu.Unlock()
				}()
				if errServe := p.serve(conn); errServe != nil {
					p.mu.Lock()
					p.data.errors = append(p.data.errors, errServe.Error())
					p.mu.Unlock()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-p.accepted
		p.mu.Lock()
		for conn := range p.conns {
			_ = conn.Close()
		}
		p.mu.Unlock()
		p.workers.Wait()
	})
	return p
}

func (p *xaiProxyUpgrade737Server) serve(conn net.Conn) error {
	reader := bufio.NewReader(conn)
	target, rejected, errHandshake := p.handshake(conn, reader)
	if errHandshake != nil {
		return errHandshake
	}
	if rejected {
		return nil
	}
	if target != p.backend {
		return fmt.Errorf("unexpected proxy target %q", target)
	}
	upstream, errDial := net.Dial("tcp", p.backend)
	if errDial != nil {
		return errDial
	}
	defer func() { _ = upstream.Close() }()
	p.mu.Lock()
	p.data.targets = append(p.data.targets, target)
	p.data.sources = append(p.data.sources, upstream.LocalAddr().String())
	p.mu.Unlock()
	if p.scheme == "http" {
		_, errHandshake = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	} else {
		_, errHandshake = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
	}
	if errHandshake != nil {
		return errHandshake
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, reader)
		_ = upstream.Close()
		close(done)
	}()
	_, _ = io.Copy(conn, upstream)
	_ = conn.Close()
	<-done
	return nil
}

func (p *xaiProxyUpgrade737Server) handshake(conn net.Conn, reader *bufio.Reader) (string, bool, error) {
	if p.scheme == "http" {
		req, errRead := http.ReadRequest(reader)
		if errRead != nil {
			return "", false, errRead
		}
		if req.Method != http.MethodConnect {
			return "", false, fmt.Errorf("expected HTTP CONNECT")
		}
		if p.reject {
			p.mu.Lock()
			p.data.rejected++
			p.mu.Unlock()
			_, errWrite := io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			return "", true, errWrite
		}
		return req.Host, false, nil
	}
	var greeting [2]byte
	if _, errRead := io.ReadFull(reader, greeting[:]); errRead != nil {
		return "", false, errRead
	}
	methods := make([]byte, int(greeting[1]))
	if _, errRead := io.ReadFull(reader, methods); errRead != nil {
		return "", false, errRead
	}
	if greeting[0] != 5 || !bytes.Contains(methods, []byte{2}) {
		return "", false, fmt.Errorf("expected SOCKS5 username/password authentication")
	}
	if _, errWrite := conn.Write([]byte{5, 2}); errWrite != nil {
		return "", false, errWrite
	}
	if _, errRead := io.ReadFull(reader, greeting[:]); errRead != nil {
		return "", false, errRead
	}
	username := make([]byte, int(greeting[1]))
	if _, errRead := io.ReadFull(reader, username); errRead != nil {
		return "", false, errRead
	}
	length, errRead := reader.ReadByte()
	if errRead != nil {
		return "", false, errRead
	}
	password := make([]byte, int(length))
	if _, errRead = io.ReadFull(reader, password); errRead != nil {
		return "", false, errRead
	}
	if greeting[0] != 1 || string(username) != "fixture-user" || string(password) != "fixture-password" {
		p.mu.Lock()
		p.data.rejected++
		p.mu.Unlock()
		_, errWrite := conn.Write([]byte{1, 1})
		return "", true, errWrite
	}
	if _, errWrite := conn.Write([]byte{1, 0}); errWrite != nil {
		return "", false, errWrite
	}
	var command [4]byte
	if _, errRead = io.ReadFull(reader, command[:]); errRead != nil {
		return "", false, errRead
	}
	if command[0] != 5 || command[1] != 1 || command[2] != 0 {
		return "", false, fmt.Errorf("expected SOCKS5 CONNECT")
	}
	var host string
	switch command[3] {
	case 1:
		var ip [4]byte
		if _, errRead = io.ReadFull(reader, ip[:]); errRead != nil {
			return "", false, errRead
		}
		host = net.IP(ip[:]).String()
	case 3:
		length, errRead = reader.ReadByte()
		if errRead != nil {
			return "", false, errRead
		}
		name := make([]byte, int(length))
		if _, errRead = io.ReadFull(reader, name); errRead != nil {
			return "", false, errRead
		}
		host = string(name)
	default:
		return "", false, fmt.Errorf("unsupported SOCKS5 address type")
	}
	var port [2]byte
	if _, errRead = io.ReadFull(reader, port[:]); errRead != nil {
		return "", false, errRead
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), false, nil
}
