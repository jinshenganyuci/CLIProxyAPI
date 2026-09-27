package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Admission is observed at the actual upstream frame boundary. A busy steer
// must stay local while its parent continues, and successors consume reservations.
func TestCodexDuplexAdmissionReservationsAndIdleRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	frames := make(chan []byte, 32)
	events := make(chan []byte, 32)
	serverDone := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					return
				}
				frames <- payload
			}
		}()
		for {
			select {
			case payload := <-events:
				if errWrite := conn.WriteMessage(websocket.TextMessage, payload); errWrite != nil {
					return
				}
			case <-readDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}))
	defer func() { cancel(); upstream.Close() }()

	var mu sync.Mutex
	occupied, maximum, limit := 1, 1, 2
	newRelease := func() func() {
		var once sync.Once
		return func() { once.Do(func() { mu.Lock(); occupied--; mu.Unlock() }) }
	}
	acquire := func(context.Context) (func(), error) {
		mu.Lock()
		defer mu.Unlock()
		if occupied >= limit {
			return nil, &auth.Error{Code: "credential_concurrency_exceeded", HTTPStatus: 429, Message: "credential busy"}
		}
		occupied++
		if occupied > maximum {
			maximum = occupied
		}
		return newRelease(), nil
	}
	assertOccupied := func(want int) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if occupied != want || maximum > 2 {
			t.Fatalf("occupied=%d want=%d maximum=%d", occupied, want, maximum)
		}
	}
	input := make(chan core.WebsocketInput, 16)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
	ctx = core.WithWebsocketResponseAdmission(ctx, &core.WebsocketResponseAdmission{ReleaseInitial: newRelease(), Acquire: acquire})
	exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
	exec.store = &codexWebsocketSessionStore{}
	credential := &auth.Auth{ID: "admission-test", Provider: "codex", ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": upstream.URL}}
	stream, errExecute := exec.ExecuteStream(ctx, credential, core.Request{Model: "gpt-test", Payload: []byte(`{"model":"gpt-test","input":[]}`)}, core.Options{SourceFormat: translator.FormatOpenAIResponse})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	readFrame := func(want string) {
		t.Helper()
		select {
		case payload := <-frames:
			if got := gjson.GetBytes(payload, "type").String(); got != want {
				t.Fatalf("upstream frame=%s, want %s", payload, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("upstream never received %s", want)
		}
	}
	readEvent := func(want string) []byte {
		t.Helper()
		select {
		case chunk := <-stream.Chunks:
			if chunk.Err != nil || gjson.GetBytes(chunk.Payload, "type").String() != want {
				t.Fatalf("downstream chunk=%s error=%v, want %s", chunk.Payload, chunk.Err, want)
			}
			return chunk.Payload
		case <-time.After(3 * time.Second):
			t.Fatalf("downstream never received %s", want)
			return nil
		}
	}
	emit := func(kind, id string) {
		events <- []byte(fmt.Sprintf(`{"type":%q,"response":{"id":%q,"output":[]}}`, kind, id))
		readEvent(kind)
	}
	sendSteer := func(parent string) {
		input <- core.WebsocketInput{Payload: []byte(fmt.Sprintf(`{"type":"response.steer","previous_response_id":%q,"input":"adjust"}`, parent))}
	}
	ack := func(kind, id, parent string) {
		events <- []byte(fmt.Sprintf(`{"type":%q,"steer":{"id":%q,"previous_response_id":%q}}`, kind, id, parent))
		readEvent(kind)
	}
	readFrame("response.create")
	emit("response.created", "r1")
	assertOccupied(1)

	// A competing key uses the remaining slot. Local rejection cannot terminate
	// the original response or send a request that already exceeds the cap.
	otherKeyRelease, errAcquire := acquire(ctx)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	sendSteer("r1")
	if payload := readEvent("error"); gjson.GetBytes(payload, "status").Int() != 429 {
		t.Fatalf("busy steer=%s", payload)
	}
	assertOccupied(2)
	otherKeyRelease()
	sendSteer("r1")
	readFrame("response.steer")
	assertOccupied(2)
	ack("response.steer.accepted", "s1", "r1")
	emit("response.completed", "r1")
	assertOccupied(1)
	emit("response.created", "r2")
	assertOccupied(1)
	emit("response.completed", "r2")
	assertOccupied(0)

	input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","input":[]}`)}
	readFrame("response.create")
	emit("response.created", "r3")
	assertOccupied(1)
	sendSteer("r3")
	readFrame("response.steer")
	ack("response.steer.accepted", "s3", "r3")
	emit("response.completed", "r3")
	assertOccupied(1)
	ack("response.steer.pending", "s3", "r3")
	assertOccupied(0)
	input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","previous_response_id":"r3","input":[{"type":"function_call_output","call_id":"tool","output":"ok"}]}`)}
	readFrame("response.create")
	emit("response.created", "r4")
	assertOccupied(1)
	emit("response.completed", "r4")
	assertOccupied(0)

	sendSteer("r4")
	readFrame("response.steer")
	assertOccupied(1)
	ack("response.steer.failed", "s4", "r4")
	assertOccupied(0)

	// A limit of one rejects steering while retaining its ongoing parent.
	mu.Lock()
	limit = 1
	mu.Unlock()
	input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","input":[]}`)}
	readFrame("response.create")
	emit("response.created", "r5")
	sendSteer("r5")
	readEvent("error")
	assertOccupied(1)
	emit("response.completed", "r5")
	assertOccupied(0)

	// Cancellation releases accepted reservations without requiring another frame.
	sendSteer("r5")
	readFrame("response.steer")
	ack("response.steer.accepted", "s5", "r5")
	assertOccupied(1)
	cancel()
	for range stream.Chunks {
	}
	assertOccupied(0)
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("socket cleanup stalled")
	}
	select {
	case payload := <-frames:
		t.Fatalf("busy request leaked to upstream: %s", payload)
	default:
	}
}
