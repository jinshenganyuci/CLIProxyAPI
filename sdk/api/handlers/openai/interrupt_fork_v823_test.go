package openai

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestV823LocalInterruptRejectsStaleFramesAndWebsocketFallback(t *testing.T) {
	state := newResponsesLocalInterrupt()
	state.bindAuth("selected", func(id string) bool { return id == "selected" })
	state.begin(true)
	if state.deliver([]byte(`{"response_id":"before-created"}`)) {
		t.Fatal("interrupt accepted before response identity was known")
	}
	state.observe([]byte(`{"type":"response.created","response":{"id":"r1"}}`))
	if state.deliver([]byte(`{"response_id":"unknown"}`)) {
		t.Fatal("wrong response identity was accepted")
	}
	if !state.deliver([]byte(`{"response_id":"r1"}`)) {
		t.Fatal("active HTTP response cannot be interrupted")
	}
	pending := <-state.framesChan()
	state.observe([]byte(`{"type":"response.completed","response":{"id":"r1"}}`))
	if state.take(pending) {
		t.Fatal("queued interrupt survived terminal response")
	}
	state.end()
	state.begin(true)
	state.observe([]byte(`{"type":"response.created","response":{"id":"r2"}}`))
	if !state.deliver([]byte(`{"response_id":"r1"}`)) {
		t.Fatal("known completed response did not become a no-op")
	}
	select {
	case <-state.framesChan():
		t.Fatal("late interrupt queued against next response")
	default:
	}
	if state.take(pending) {
		t.Fatal("old turn frame affected next turn")
	}
	state.end()
	state.begin(false)
	state.observe([]byte(`{"type":"response.created","response":{"id":"r-ws"}}`))
	if state.deliver([]byte(`{"response_id":"r-ws"}`)) {
		t.Fatal("websocket response fell through to HTTP cancellation")
	}
	state.end()
	for n := 0; n < responsesInterruptTerminalLimit+20; n++ {
		state.observe([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"terminal-%d"}}`, n)))
	}
	if len(state.terminalIDs) != responsesInterruptTerminalLimit || len(state.terminalOrder) != responsesInterruptTerminalLimit {
		t.Fatal("terminal interrupt history is unbounded")
	}
	if state.deliver([]byte(`{"response_id":"terminal-0"}`)) {
		t.Fatal("oldest bounded history entry was not evicted")
	}
}

func TestV824LocalInterruptHistoryRetainsFixedSizeDigests(t *testing.T) {
	state := newResponsesLocalInterrupt()
	longID := strings.Repeat("x", 256*1024) + "-a"
	otherID := strings.Repeat("x", 256*1024) + "-b"
	remember := func(id string) {
		state.observe([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q}}`, id)))
	}
	deliver := func(id string) bool {
		return state.deliver([]byte(fmt.Sprintf(`{"response_id":%q}`, id)))
	}
	remember(longID)
	remember(longID)
	if len(state.terminalIDs) != 1 || !deliver(longID) || deliver(otherID) {
		t.Fatal("terminal history lost exact response identity or duplicate handling")
	}
	remember(otherID)
	if !deliver(otherID) {
		t.Fatal("different long response ID was not retained")
	}
	for _, retained := range state.terminalOrder {
		if len(retained) != sha256.Size {
			t.Fatalf("terminal history retains %d bytes per ID, want fixed digest", len(retained))
		}
	}
	for n := range responsesInterruptTerminalLimit {
		remember(fmt.Sprintf("later-%d", n))
	}
	if deliver(longID) || deliver(otherID) || len(state.terminalIDs) != responsesInterruptTerminalLimit {
		t.Fatal("long response IDs did not follow bounded FIFO eviction")
	}
	select {
	case <-state.framesChan():
		t.Fatal("terminal history lookup queued an interrupt")
	default:
	}
}

func TestV823HTTPInterruptPreservesProxyLeaseAccountingAndNextTurn(t *testing.T) {
	const model = "v823-http-interrupt"
	var hits, fallbackHits atomic.Int32
	canceled := make(chan string, 3)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := hits.Add(1)
		id := fmt.Sprintf("http-%d", turn)
		if !r.URL.IsAbs() || r.URL.Host != "v823-upstream.invalid" || r.Header.Get("Authorization") != "Bearer synthetic-v823" {
			t.Errorf("interrupt request escaped bound proxy: %s", r.URL)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":%q}}\n\n", id)
		w.(http.Flusher).Flush()
		if turn == 2 {
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":7,\"total_tokens\":12}}}\n\n", id)
			return
		}
		<-r.Context().Done()
		canceled <- id
	}))
	t.Cleanup(proxy.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		http.Error(w, "unexpected fallback", 502)
	}))
	t.Cleanup(fallback.Close)
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	id := t.Name() + "-a"
	auth, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: proxy.URL, Attributes: map[string]string{"api_key": "synthetic-v823", "base_url": "http://v823-upstream.invalid", "websockets": "false"}, Metadata: map[string]any{"concurrency_limit": 3, "codex_identity_namespace": "stable-v823"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	for range 2 {
		release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), auth)
		if errAcquire != nil {
			t.Fatal(errAcquire)
		}
		t.Cleanup(release)
	}
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	host := &issue6332LifecycleHost{completions: make(chan pluginapi.RequestCompletion, 8)}
	base.SetPluginHost(host)
	capture := v820RegisterUsage(t, "codex")
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userApiKey", strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		c.Next()
	})
	router.GET("/v1/responses", NewOpenAIResponsesAPIHandler(base).ResponsesWebsocket)
	gateway := httptest.NewServer(router)
	t.Cleanup(gateway.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer synthetic-v823-owner"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	send := func(payload string) {
		t.Helper()
		if errWrite := client.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	read := func(kind, id string) []byte {
		t.Helper()
		_, payload, errRead := client.ReadMessage()
		if errRead != nil {
			t.Fatal(errRead)
		}
		if gjson.GetBytes(payload, "type").String() != kind || (id != "" && gjson.GetBytes(payload, "response.id").String() != id) {
			t.Fatalf("expected %s/%s, got %s", kind, id, payload)
		}
		return payload
	}
	create := func() {
		send(`{"type":"response.create","model":"v823-http-interrupt","input":[{"role":"user","content":"hello"}]}`)
	}
	waitLease := func(want int) {
		t.Helper()
		waitCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
		defer stop()
		for {
			active, limit := manager.CredentialConcurrencySnapshot(id)
			if active == want && limit == 3 {
				return
			}
			if waitCtx.Err() != nil {
				t.Fatalf("interrupt lease did not settle: %d/%d want %d/3", active, limit, want)
			}
			runtime.Gosched()
		}
	}
	create()
	read("response.created", "http-1")
	waitLease(3)
	foreign, _, errForeign := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer synthetic-v823-other"}})
	if errForeign != nil {
		t.Fatal(errForeign)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	_ = foreign.SetReadDeadline(time.Now().Add(5 * time.Second))
	if errWrite := foreign.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.interrupt","response_id":"http-1"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	_, foreignReply, errReadForeign := foreign.ReadMessage()
	if errReadForeign != nil || gjson.GetBytes(foreignReply, "type").String() != "error" {
		t.Fatalf("foreign session accepted another response ID: %s %v", foreignReply, errReadForeign)
	}
	_ = foreign.Close()
	send(`{"type":"response.interrupt","response_id":"wrong-id"}`)
	read("error", "")
	select {
	case got := <-canceled:
		t.Fatalf("wrong ID canceled %s", got)
	default:
	}
	send(`{"type":"response.interrupt","response_id":"http-1","mode":"discard_partial_items"}`)
	read("response.incomplete", "http-1")
	if got := v820Await(t, canceled); got != "http-1" {
		t.Fatal(got)
	}
	waitLease(2)
	send(`{"type":"response.interrupt","response_id":"http-1"}`)
	create()
	read("response.created", "http-2")
	read("response.completed", "http-2")
	waitLease(2)
	create()
	read("response.created", "http-3")
	waitLease(3)
	send(`{"type":"response.interrupt","response_id":"http-1"}`)
	send(`{"type":"response.interrupt","response_id":"http-2"}`)
	current, _ := manager.GetByID(id)
	changed := current.Clone()
	changed.ProxyURL = fallback.URL
	if _, errUpdate := manager.Update(t.Context(), changed); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	send(`{"type":"response.interrupt","response_id":"http-3"}`)
	read("error", "")
	select {
	case got := <-canceled:
		t.Fatalf("changed credential canceled active turn %s", got)
	default:
	}
	if _, errUpdate := manager.Update(t.Context(), current); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	send(`{"type":"response.interrupt","response_id":"http-3"}`)
	read("response.incomplete", "http-3")
	if got := v820Await(t, canceled); got != "http-3" {
		t.Fatal(got)
	}
	waitLease(2)
	successes, cancellations := 0, 0
	for range 3 {
		completion := v820Await(t, host.completions)
		switch completion.Outcome {
		case pluginapi.RequestCompletionSucceeded:
			successes++
		case pluginapi.RequestCompletionCanceled:
			cancellations++
		default:
			t.Fatalf("unexpected interrupt completion: %+v", completion)
		}
	}
	if successes != 1 || cancellations != 2 {
		t.Fatalf("interrupt created bogus lifecycles: success=%d cancel=%d", successes, cancellations)
	}
	successfulUsage, failedUsage := 0, 0
	for range 3 {
		record := v820Await(t, capture.records)
		t.Logf("interrupt usage received: failed=%t tokens=%d", record.Failed, record.Detail.TotalTokens)
		if record.AuthID != id || record.APIKey != "synthetic-v823-owner" {
			t.Fatal("interrupt usage crossed credentials")
		}
		if record.Failed {
			failedUsage++
			if record.Detail.TotalTokens != 0 {
				t.Fatalf("canceled empty turn invented tokens: %+v", record)
			}
		} else {
			successfulUsage++
			if record.Detail.TotalTokens != 12 {
				t.Fatalf("follow-up lost terminal usage: %+v", record)
			}
		}
	}
	if successfulUsage != 1 || failedUsage != 2 {
		t.Fatalf("interrupt usage outcomes: success=%d failed=%d", successfulUsage, failedUsage)
	}
	finalAuth, _ := manager.GetByID(id)
	if finalAuth.Unavailable || finalAuth.Quota.Exceeded {
		t.Fatalf("plain local interruption recorded a credential cooldown: %+v", finalAuth)
	}
	for model, state := range finalAuth.ModelStates {
		if state != nil && (state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero()) {
			t.Fatalf("plain local interruption cooled model %s: %+v", model, state)
		}
	}
	v820AssertNoMoreUsage(t, capture)
	if hits.Load() != 3 || fallbackHits.Load() != 0 {
		t.Fatalf("interrupt created upstream request or fallback: %d/%d", hits.Load(), fallbackHits.Load())
	}
}
