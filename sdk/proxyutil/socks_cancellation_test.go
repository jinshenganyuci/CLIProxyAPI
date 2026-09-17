package proxyutil

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
		if errRead == nil && (stage == "connect" || stage == "complete") {
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
		if stage == "complete" {
			if _, errWrite := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80}); errWrite != nil {
				closed <- errWrite
				return
			}
			_, errCopy := io.Copy(conn, conn)
			closed <- errCopy
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

func TestSOCKSHTTPDialContextCancellationClosesHandshake(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		for _, stage := range []string{"greeting", "authentication", "connect"} {
			t.Run(scheme+"/"+stage, func(t *testing.T) {
				address, ready, closed := blockedSOCKSProxy(t, stage)
				transport, _, errBuild := BuildHTTPTransport(scheme + "://test-user:test-password@" + address)
				if errBuild != nil {
					t.Fatal(errBuild)
				}
				defer transport.CloseIdleConnections()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					conn, errDial := transport.DialContext(ctx, "tcp", "target.invalid:443")
					if conn != nil {
						_ = conn.Close()
					}
					done <- errDial
				}()
				if errReady := awaitSOCKSResult(t, ready, "blocked handshake"); errReady != nil {
					t.Fatal(errReady)
				}
				cancel()
				if errDial := awaitSOCKSResult(t, done, "canceled SOCKS dial"); errDial == nil {
					t.Fatal("canceled handshake unexpectedly succeeded")
				}
				if errClosed := awaitSOCKSResult(t, closed, "proxy connection close"); errClosed != io.EOF {
					t.Fatalf("proxy connection did not close cleanly: %v", errClosed)
				}
			})
		}
	}
}

func TestSOCKSHTTPRequestCancellationAndTransportClose(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			var directHits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				directHits.Add(1)
				writer.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			address, ready, closed := blockedSOCKSProxy(t, "greeting")
			transport, _, errBuild := BuildHTTPTransport(scheme + "://" + address)
			if errBuild != nil {
				t.Fatal(errBuild)
			}
			defer transport.CloseIdleConnections()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			done := make(chan error, 1)
			go func() {
				response, errRequest := (&http.Client{Transport: transport}).Do(request)
				if response != nil {
					_ = response.Body.Close()
				}
				done <- errRequest
			}()
			if errReady := awaitSOCKSResult(t, ready, "HTTP SOCKS greeting"); errReady != nil {
				t.Fatal(errReady)
			}
			cancel()
			if errRequest := awaitSOCKSResult(t, done, "canceled HTTP request"); errRequest == nil || !strings.Contains(errRequest.Error(), "context canceled") {
				t.Fatalf("expected canceled HTTP request, got %v", errRequest)
			}
			// net/http deliberately detaches pending dials from request cancellation.
			// Closing idle connections also cancels dials that have no waiting request.
			transport.CloseIdleConnections()
			if errClosed := awaitSOCKSResult(t, closed, "unused transport dial close"); errClosed != io.EOF {
				t.Fatalf("proxy connection did not close cleanly: %v", errClosed)
			}
			if directHits.Load() != 0 {
				t.Fatal("canceled SOCKS request fell back to direct transport")
			}
		})
	}
}

func TestSOCKSHTTPDialContextCancellationLeavesEstablishedConnectionUsable(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			address, ready, _ := blockedSOCKSProxy(t, "complete")
			transport, _, errBuild := BuildHTTPTransport(scheme + "://test-user:test-password@" + address)
			if errBuild != nil {
				t.Fatal(errBuild)
			}
			defer transport.CloseIdleConnections()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn, errDial := transport.DialContext(ctx, "tcp", "target.invalid:443")
			if errDial != nil {
				t.Fatal(errDial)
			}
			defer func() { _ = conn.Close() }()
			if errReady := awaitSOCKSResult(t, ready, "completed SOCKS handshake"); errReady != nil {
				t.Fatal(errReady)
			}
			cancel()
			done := make(chan error, 1)
			go func() {
				if _, errWrite := conn.Write([]byte("ping")); errWrite != nil {
					done <- errWrite
					return
				}
				data := make([]byte, 4)
				_, errRead := io.ReadFull(conn, data)
				if errRead == nil && string(data) != "ping" {
					errRead = fmt.Errorf("unexpected echo: %q", data)
				}
				done <- errRead
			}()
			if errEcho := awaitSOCKSResult(t, done, "data after dial context cancellation"); errEcho != nil {
				t.Fatalf("dial cancellation affected established connection: %v", errEcho)
			}
		})
	}
}
