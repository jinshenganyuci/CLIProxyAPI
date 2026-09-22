package executor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// blockedSOCKSProxy stops at a selected SOCKS handshake response and observes
// whether cancellation closes the real TCP connection on the proxy side.
func blockedSOCKSProxy(t *testing.T, stage string) (string, <-chan error, <-chan error) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	ready := make(chan error, 1)
	closed := make(chan error, 1)
	accepted := make(chan net.Conn, 1)
	t.Cleanup(func() {
		_ = listener.Close()
		if conn, ok := <-accepted; ok {
			_ = conn.Close()
		}
	})
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			close(accepted)
			ready <- errAccept
			return
		}
		accepted <- conn
		close(accepted)
		defer func() { _ = conn.Close() }()
		readN := func(size int) ([]byte, error) {
			data := make([]byte, size)
			_, errRead := io.ReadFull(conn, data)
			return data, errRead
		}
		greeting, errRead := readN(2)
		if errRead == nil {
			_, errRead = readN(int(greeting[1]))
		}
		if errRead == nil && stage != "greeting" {
			_, errRead = conn.Write([]byte{5, 2})
			if errRead == nil {
				var auth []byte
				auth, errRead = readN(2)
				if errRead == nil {
					_, errRead = readN(int(auth[1]))
				}
				if errRead == nil {
					var passwordSize []byte
					passwordSize, errRead = readN(1)
					if errRead == nil {
						_, errRead = readN(int(passwordSize[0]))
					}
				}
			}
		}
		if errRead == nil && stage == "connect" {
			_, errRead = conn.Write([]byte{1, 0})
			if errRead == nil {
				var request []byte
				request, errRead = readN(4)
				if errRead == nil {
					size := 4
					switch request[3] {
					case 3:
						var length []byte
						length, errRead = readN(1)
						if errRead == nil {
							size = int(length[0])
						}
					case 4:
						size = 16
					}
					if errRead == nil {
						_, errRead = readN(size + 2)
					}
				}
			}
		}
		ready <- errRead
		if errRead != nil {
			return
		}
		data, errClosed := readN(1)
		if errClosed == nil {
			errClosed = fmt.Errorf("unexpected data after blocked handshake: %x", data)
		}
		closed <- errClosed
	}()
	return listener.Addr().String(), ready, closed
}

func awaitSOCKSResult(t *testing.T, result <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func TestCodexSOCKSWebsocketCancellationClosesHandshake(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		for _, stage := range []string{"greeting", "authentication", "connect"} {
			t.Run(scheme+"/"+stage, func(t *testing.T) {
				var directHits, globalProxyHits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					directHits.Add(1)
					writer.WriteHeader(http.StatusBadGateway)
				}))
				defer upstream.Close()
				globalProxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					globalProxyHits.Add(1)
					writer.WriteHeader(http.StatusBadGateway)
				}))
				defer globalProxy.Close()
				address, ready, closed := blockedSOCKSProxy(t, stage)
				cfg := &config.Config{}
				cfg.ProxyURL = globalProxy.URL
				auth := &cliproxyauth.Auth{ID: "socks-cancellation", ProxyURL: scheme + "://test-user:test-password@" + address}
				dialer := newProxyAwareWebsocketDialer(context.Background(), cfg, auth)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					conn, response, errDial := dialer.DialContext(ctx, "ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
					if conn != nil {
						_ = conn.Close()
					}
					if response != nil && response.Body != nil {
						_ = response.Body.Close()
					}
					done <- errDial
				}()
				if errReady := awaitSOCKSResult(t, ready, "blocked WebSocket SOCKS handshake"); errReady != nil {
					t.Fatal(errReady)
				}
				cancel()
				if errDial := awaitSOCKSResult(t, done, "canceled WebSocket dial"); errDial == nil {
					t.Fatal("canceled SOCKS handshake unexpectedly connected a WebSocket")
				}
				if errClosed := awaitSOCKSResult(t, closed, "WebSocket proxy connection close"); errClosed != io.EOF {
					t.Fatalf("proxy connection did not close cleanly: %v", errClosed)
				}
				if directHits.Load() != 0 || globalProxyHits.Load() != 0 {
					t.Fatalf("canceled credential proxy fell back: direct=%d global=%d", directHits.Load(), globalProxyHits.Load())
				}
			})
		}
	}
}
