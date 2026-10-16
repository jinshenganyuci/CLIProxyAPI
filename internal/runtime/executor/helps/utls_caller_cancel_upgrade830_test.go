package helps

import (
	"bufio"
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestV830ClaudePooledTLSConnectionOutlivesOriginalCaller(t *testing.T) {
	certificate := newResumptionTestCertificate(t)
	roots := x509.NewCertPool()
	roots.AddCert(certificate.Leaf)
	resume := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/first" {
			_, _ = io.WriteString(w, "first")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ready\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-resume:
			_, _ = io.WriteString(w, "data: finished\n\n")
		case <-req.Context().Done():
		}
	}))
	server.TLS = &stdtls.Config{Certificates: []stdtls.Certificate{certificate}}
	server.StartTLS()
	t.Cleanup(server.Close)
	var dials atomic.Int32
	dialer := contextDialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	})
	transport := newClaudeCodeRoundTripperForDialerAndRoots(dialer, roots)
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: &tlsHandshakeTimeoutRoundTripper{
		base: &fallbackRoundTripper{anthropic: transport}, timeout: time.Second,
	}}
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	firstReq, errRequest := http.NewRequestWithContext(firstCtx, http.MethodGet, "https://api.anthropic.com/first", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	first, errFirst := client.Do(firstReq)
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	firstBody, errRead := io.ReadAll(first.Body)
	if errClose := first.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if errRead != nil || string(firstBody) != "first" {
		t.Fatalf("first body = %q, error %v", firstBody, errRead)
	}
	secondReq, errRequest := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.anthropic.com/second", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	second, errSecond := client.Do(secondReq)
	if errSecond != nil {
		t.Fatal(errSecond)
	}
	defer func() {
		if errClose := second.Body.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	reader := bufio.NewReader(second.Body)
	if firstLine, errLine := reader.ReadString('\n'); errLine != nil || firstLine != "data: ready\n" {
		t.Fatalf("SSE start = %q, error %v", firstLine, errLine)
	}
	cancelFirst()
	close(resume)
	remainder, errRemainder := io.ReadAll(reader)
	if errRemainder != nil || string(remainder) != "\ndata: finished\n\n" {
		t.Fatalf("SSE after old caller cancel = %q, error %v", remainder, errRemainder)
	}
	if dials.Load() != 1 {
		t.Fatalf("connection was not reused: dials = %d, want 1", dials.Load())
	}
}
