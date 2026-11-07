package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/traffic"
)

func TestResponsesWebsocketTrafficCountsPayloadAndTerminalOnce(t *testing.T) {
	collector, errOpen := traffic.Open(traffic.Options{Enabled: true, DisableBackground: true})
	if errOpen != nil {
		t.Fatalf("open traffic collector: %v", errOpen)
	}
	defer func() { _ = collector.Close(context.Background()) }()
	meter := collector.NewMeter()
	meter.Bind(traffic.Identity{Provider: "config-inline", Key: "synthetic-responses-caller"})
	const interrupt = `{"type":"response.interrupt","response_id":"synthetic"}`
	const terminal = `{"type":"error","message":"synthetic terminal"}`
	payloads := [][]byte{[]byte(strings.Repeat("payload", 4096)), {1, 2, 3, 4}}
	serverDone := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		upgrader := responsesWebsocketUpgrader
		upgrader.EnableCompression = true
		conn, errUpgrade := upgrader.Upgrade(w, request, nil)
		if errUpgrade != nil {
			serverDone <- errUpgrade
			return
		}
		writer := newResponsesWebsocketWriter(conn)
		writer.meter = meter
		ctx, cancel := context.WithCancelCause(traffic.WithMeter(request.Context(), meter))
		var epoch atomic.Uint64
		input, inputDone := readResponsesWebsocketInput(ctx, cancel, conn, func([]byte) error { return nil }, newResponsesLocalInterrupt(), writer, nil, &epoch)
		var result error
		defer func() {
			cancel(nil)
			_ = conn.Close()
			<-inputDone
			serverDone <- result
		}()
		if result = writer.writePing(); result != nil {
			return
		}
		for _, want := range payloads {
			message, ok := <-input
			if !ok || string(message.Payload) != string(want) {
				result = fmt.Errorf("input payload = %q, want %q", message.Payload, want)
				return
			}
			if result = writeResponsesWebsocketPayload(writer, nil, message.Payload, time.Now()); result != nil {
				return
			}
		}
		if _, result = writer.closeWithPayload([]byte(terminal)); result != nil {
			return
		}
		if errWrite := writeResponsesWebsocketPayload(writer, nil, []byte("not delivered"), time.Now()); !errors.Is(errWrite, websocket.ErrCloseSent) {
			result = fmt.Errorf("write after terminal = %v, want ErrCloseSent", errWrite)
		}
	}))
	defer server.Close()
	dialer := websocket.Dialer{EnableCompression: true}
	client, response, errDial := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = client.Close() }()
	if !strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		t.Fatal("fixture did not negotiate message compression")
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if errWrite := client.WriteMessage(websocket.TextMessage, []byte(interrupt)); errWrite != nil {
		t.Fatalf("write interrupt: %v", errWrite)
	}
	for index, payload := range payloads {
		kind := websocket.TextMessage
		if index == 1 {
			kind = websocket.BinaryMessage
		}
		if errWrite := client.WriteMessage(kind, payload); errWrite != nil {
			t.Fatalf("write payload: %v", errWrite)
		}
		_, actual, errRead := client.ReadMessage()
		if errRead != nil || string(actual) != string(payload) {
			t.Fatalf("read echoed payload: bytes=%d err=%v", len(actual), errRead)
		}
	}
	_, actual, errRead := client.ReadMessage()
	if errRead != nil || string(actual) != terminal {
		t.Fatalf("terminal payload = %q, error=%v", actual, errRead)
	}
	if errServer := <-serverDone; errServer != nil {
		t.Fatalf("websocket server: %v", errServer)
	}
	rows := collector.Keys(traffic.KeyQuery{Limit: 20})
	if len(rows.Keys) != 1 {
		t.Fatalf("traffic keys = %+v", rows)
	}
	got := rows.Keys[0].Traffic.Total.Protocols[traffic.WebSocket]
	wantPayload := len(payloads[0]) + len(payloads[1])
	if got.RxBytes != uint64(wantPayload+len(interrupt)) || got.TxBytes != uint64(wantPayload+len(terminal)) {
		t.Fatalf("websocket payload bytes = %+v, want rx=%d tx=%d", got, wantPayload+len(interrupt), wantPayload+len(terminal))
	}
}
