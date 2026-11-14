package wstunnel

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/traffic"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func TestHubTraffic_NativeTenantPayloadsAfterHandshake(t *testing.T) {
	collector, errOpen := traffic.Open(traffic.Options{Enabled: true, DisableBackground: true})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() {
		if errClose := collector.Close(context.Background()); errClose != nil {
			t.Error(errClose)
		}
	})
	hub := NewHub(Options{})
	t.Cleanup(hub.Close)
	const keyAlpha, keyBeta = "synthetic-tunnel-alpha", "synthetic-tunnel-beta"
	hub.UpdateAllowedAPIKeys([]string{keyAlpha, keyBeta})
	handled := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meter := collector.NewMeter()
		hub.ServeHTTP(w, r.WithContext(traffic.WithMeter(r.Context(), meter)))
		meter.Finish()
		handled <- struct{}{}
	}))
	t.Cleanup(srv.Close)

	// Only the credential actually accepted by the tunnel may create an identity.
	_, rejected, errDial := websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, url.Values{
		"key": {"synthetic-plugin-only"}, "role": {"client"}, "uuid": {"rejected"}, "name": {"Rejected"},
	}), nil)
	closeHTTPResponseBody(t, rejected)
	if errDial == nil || rejected == nil || rejected.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unlisted key was not rejected: %v, %v", rejected, errDial)
	}
	waitTunnelSignal(t, handled, "rejected handshake")
	if rows := collector.Keys(traffic.KeyQuery{Limit: 20}); rows.Total != 0 {
		t.Fatalf("rejected credential created traffic identities: %+v", rows)
	}

	collector.SyncNativeKeys([]traffic.Identity{
		{Provider: sdkaccess.DefaultAccessProviderName, Key: keyAlpha, Name: "alpha"},
		{Provider: sdkaccess.DefaultAccessProviderName, Key: keyBeta, Name: "beta"},
	})
	serverAlpha := dialRole(t, srv.URL, keyAlpha, RoleServer, "server", "Alpha")
	waitTunnelSignal(t, handled, "alpha server handshake")
	_ = dialRole(t, srv.URL, keyBeta, RoleServer, "server", "Beta")
	waitTunnelSignal(t, handled, "beta server handshake")
	clientAlpha := dialRole(t, srv.URL, keyAlpha, RoleClient, "client", "Alpha")
	waitTunnelSignal(t, handled, "alpha client handshake")
	clientBeta, responseBeta, errBeta := websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, url.Values{
		"key": {keyBeta}, "role": {"client"}, "uuid": {"client"}, "name": {"Beta"},
	}), http.Header{"Authorization": {"Bearer synthetic-invalid-first"}})
	closeHTTPResponseBody(t, responseBeta)
	if errBeta != nil {
		t.Fatal(errBeta)
	}
	registerCleanupConn(t, clientBeta)
	waitTunnelSignal(t, handled, "beta fallback handshake")

	readPayload := func(conn *websocket.Conn, kind int) []byte {
		t.Helper()
		gotKind, payload, errRead := readTunnelTestMessage(conn)
		if errRead != nil || gotKind != kind {
			t.Fatalf("read payload: type=%d err=%v", gotKind, errRead)
		}
		return payload
	}
	alphaTx := len(readPayload(clientAlpha, websocket.TextMessage))
	betaTx := len(readPayload(clientBeta, websocket.TextMessage))

	// Both handlers have finished their request meters. Each relay leg still
	// belongs to that native key and counts once, including the tunnel envelope.
	packet, errEncode := EncodeDataFrame(PayloadTypeBinary, "client", "server", []byte("payload"))
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	if errWrite := clientAlpha.WriteMessage(websocket.BinaryMessage, packet); errWrite != nil {
		t.Fatal(errWrite)
	}
	forwarded := readPayload(serverAlpha, websocket.BinaryMessage)
	if !bytes.Equal(forwarded, packet) {
		t.Fatal("tunnel changed the forwarded payload")
	}
	alphaTx += len(forwarded)
	alphaRx := len(packet)
	query := []byte(`{"type":"list_servers","request_id":"beta-only"}`)
	if errWrite := clientBeta.WriteMessage(websocket.TextMessage, query); errWrite != nil {
		t.Fatal(errWrite)
	}
	betaTx += len(readPayload(clientBeta, websocket.TextMessage))

	// A nonempty Ping/Pong remains outside data-message accounting. A reader
	// processes the Pong first, then a data response ends that reader's wait.
	pong := make(chan struct{}, 1)
	clientAlpha.SetPongHandler(func(string) error { pong <- struct{}{}; return nil })
	type readResult struct {
		kind    int
		payload []byte
		err     error
	}
	response := make(chan readResult, 1)
	go func() {
		kind, payload, errRead := clientAlpha.ReadMessage()
		response <- readResult{kind, payload, errRead}
	}()
	if errWrite := clientAlpha.WriteControl(websocket.PingMessage, []byte("transport-only"), time.Time{}); errWrite != nil {
		t.Fatal(errWrite)
	}
	waitTunnelSignal(t, pong, "uncounted Pong")
	if errWrite := clientAlpha.WriteMessage(websocket.TextMessage, query); errWrite != nil {
		t.Fatal(errWrite)
	}
	select {
	case result := <-response:
		if result.err != nil || result.kind != websocket.TextMessage {
			t.Fatalf("post-Pong response: type=%d err=%v", result.kind, result.err)
		}
		alphaTx += len(result.payload)
		alphaRx += len(query)
	case <-time.After(5 * time.Second):
		t.Fatal("post-Pong data response did not arrive")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if errClose := hub.CloseAndWait(ctx); errClose != nil {
		t.Fatal(errClose)
	}
	want := map[string]traffic.Bytes{
		"alpha": {RxBytes: uint64(alphaRx), TxBytes: uint64(alphaTx), TotalBytes: uint64(alphaRx + alphaTx)},
		"beta":  {RxBytes: uint64(len(query)), TxBytes: uint64(betaTx), TotalBytes: uint64(len(query) + betaTx)},
	}
	rows := collector.Keys(traffic.KeyQuery{Limit: 20})
	if rows.Total != 2 {
		t.Fatalf("traffic identities = %d, want 2", rows.Total)
	}
	for _, row := range rows.Keys {
		if row.Group != "native" || row.Provider != sdkaccess.DefaultAccessProviderName {
			t.Fatalf("wrong native identity: %+v", row)
		}
		if got := row.Traffic.Total.Protocols[traffic.WebSocket]; got != want[row.Name] {
			t.Fatalf("%s traffic = %+v, want %+v", row.Name, got, want[row.Name])
		}
		for _, secret := range []string{keyAlpha, keyBeta} {
			if strings.Contains(row.ID+row.Name+row.Mask, secret) {
				t.Fatal("raw API key escaped into traffic metadata")
			}
		}
	}
	if got := collector.Summary().Unattributed.Total.TotalBytes; got != 0 {
		t.Fatalf("authenticated tunnel traffic became unattributed: %d", got)
	}
}

func TestHubCloseAndWait_TracksHandshakeAndRevokedWriter(t *testing.T) {
	t.Run("accepted handshake", func(t *testing.T) {
		hub := NewHub(Options{})
		hub.UpdateAllowedAPIKeys([]string{"key"})
		reserved, resume := make(chan struct{}), make(chan struct{})
		var once sync.Once
		resumeHandshake := func() { once.Do(func() { close(resume) }) }
		hub.testOnAfterReserve = func() { close(reserved); <-resume }
		srv := httptest.NewServer(hub)
		t.Cleanup(srv.Close)
		t.Cleanup(hub.Close)
		t.Cleanup(resumeHandshake)
		dialDone := make(chan struct{})
		go func() {
			defer close(dialDone)
			conn, response, errDial := websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, url.Values{
				"key": {"key"}, "role": {"client"}, "uuid": {"pending"}, "name": {"Pending"},
			}), nil)
			closeHTTPResponseBody(t, response)
			if errDial == nil {
				assertTunnelClose(t, conn, websocket.CloseGoingAway)
				_ = conn.Close()
			}
		}()
		waitTunnelSignal(t, reserved, "reserved handshake")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if errClose := hub.CloseAndWait(ctx); !errors.Is(errClose, context.Canceled) {
			t.Fatalf("CloseAndWait() = %v, want canceled while handshake remains", errClose)
		}
		resumeHandshake()
		waitTunnelSignal(t, dialDone, "post-close handshake rejection")
		deadline, cancelDeadline := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelDeadline()
		if errClose := hub.CloseAndWait(deadline); errClose != nil {
			t.Fatal(errClose)
		}
	})
	t.Run("revoked writer", func(t *testing.T) {
		hub := NewHub(Options{})
		hub.UpdateAllowedAPIKeys([]string{"key"})
		forceClose := make(chan time.Time)
		hub.testForceClose = forceClose
		var once sync.Once
		reclaim := func() { once.Do(func() { close(forceClose) }) }
		t.Cleanup(reclaim)
		srv := newGatedTunnelServer(t, hub)
		client := dialRole(t, srv.URL, "key", RoleClient, "client", "Client")
		_ = readTextJSONFrame(t, client)
		p := lookupTunnelPeer(t, hub, "key", "client")
		transport := rawTransportConn(p.conn.UnderlyingConn()).(*gatedTunnelConn)
		transport.arm()
		hub.UpdateAllowedAPIKeys(nil)
		waitTunnelSignal(t, transport.blocked, "revoked peer close write")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if errClose := hub.CloseAndWait(ctx); !errors.Is(errClose, context.Canceled) {
			t.Fatalf("CloseAndWait() = %v, want canceled while removed peer remains", errClose)
		}
		reclaim()
		deadline, cancelDeadline := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelDeadline()
		if errClose := hub.CloseAndWait(deadline); errClose != nil {
			t.Fatal(errClose)
		}
		select {
		case <-p.writerDone:
		default:
			t.Fatal("CloseAndWait returned before the revoked writer exited")
		}
	})
}

// blockedHandshakeListener reuses the existing deterministic write gate before
// the first HTTP 101 write, when there is no committed peer to reclaim yet.
type blockedHandshakeListener struct {
	net.Listener
	accepted chan *gatedTunnelConn
}

func (l blockedHandshakeListener) Accept() (net.Conn, error) {
	conn, errAccept := l.Listener.Accept()
	if errAccept != nil {
		return nil, errAccept
	}
	guarded := &gatedTunnelConn{Conn: conn, closed: make(chan struct{})}
	guarded.arm()
	l.accepted <- guarded
	return guarded, nil
}

func TestHubCloseAndWait_ReclaimsBlockedUpgrade(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"key"})
	accepted := make(chan *gatedTunnelConn, 1)
	srv := httptest.NewUnstartedServer(hub)
	srv.Listener = blockedHandshakeListener{Listener: srv.Listener, accepted: accepted}
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(hub.Close)
	dialDone := make(chan struct{})
	go func() {
		defer close(dialDone)
		conn, response, errDial := websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, url.Values{
			"key": {"key"}, "role": {"client"}, "uuid": {"blocked-upgrade"}, "name": {"Blocked"},
		}), nil)
		closeHTTPResponseBody(t, response)
		if errDial == nil {
			_ = conn.Close()
			t.Error("blocked upgrade unexpectedly succeeded")
		}
	}()
	var transport *gatedTunnelConn
	select {
	case transport = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel connection was not accepted")
	}
	t.Cleanup(func() { _ = transport.Close() })
	waitTunnelSignal(t, transport.blocked, "blocked HTTP 101 write")
	hub.mu.RLock()
	pending := len(hub.handshakes)
	hub.mu.RUnlock()
	if pending != 1 {
		t.Fatalf("pending hijacked connections = %d, want 1", pending)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		// Background models default immediate server shutdown: no deadline is
		// needed to free the raw connection and complete the accepted handler.
		if errClose := hub.CloseAndWait(context.Background()); errClose != nil {
			t.Error(errClose)
		}
	}()
	waitTunnelSignal(t, stopped, "shutdown with blocked upgrade")
	waitTunnelSignal(t, transport.closed, "raw hijacked transport reclamation")
	waitTunnelSignal(t, dialDone, "failed client upgrade")
}
