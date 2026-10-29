package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// A transport loss must settle both running and reserved response capacity;
// discarded operations remain correlated without replay or route fallback.
func TestV847DuplexLossPreservesProxyIdentityAndReleasesReservations(t *testing.T) {
	for _, phase := range []string{"accepted_steer", "successor", "pending_create"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			const model = "gpt-5.4"
			frames := make(chan []byte, 4)
			lose := make(chan struct{})
			var selectedHits, fallbackHits, upstreamHits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-loss847" {
					t.Errorf("wrong credential partition: %q", r.Header.Get("Authorization"))
				}
				conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				read := func() bool {
					_, body, errRead := conn.ReadMessage()
					if errRead != nil {
						t.Error(errRead)
						return false
					}
					frames <- body
					return true
				}
				write := func(body string) bool {
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(body)); errWrite != nil {
						t.Error(errWrite)
						return false
					}
					return true
				}
				if !read() || !write(`{"type":"response.created","response":{"id":"r847"}}`) || !read() {
					return
				}
				if phase != "pending_create" {
					if !write(`{"type":"response.steer.accepted","steer":{"id":"s847","previous_response_id":"r847"}}`) {
						return
					}
					if phase == "successor" {
						if !write(`{"type":"response.completed","response":{"id":"r847","output":[],"usage":{"input_tokens":11,"output_tokens":2,"total_tokens":13}}}`) || !write(`{"type":"response.created","response":{"id":"r848","previous_response_id":"r847"}}`) {
							return
						}
					}
				}
				select {
				case <-lose:
				case <-ctx.Done():
				}
				// Close without a websocket close frame: a transport loss, not policy.
			}))
			defer func() { cancel(); upstream.Close() }()
			selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				selectedHits.Add(1)
				if r.Method != http.MethodConnect || r.Host != "loss847.invalid:80" {
					t.Errorf("unexpected selected proxy request: %s %s", r.Method, r.Host)
					http.Error(w, "bad target", 400)
					return
				}
				remote, errDial := net.Dial("tcp", strings.TrimPrefix(upstream.URL, "http://"))
				if errDial != nil {
					t.Error(errDial)
					return
				}
				defer func() { _ = remote.Close() }()
				client, buffered, errHijack := w.(http.Hijacker).Hijack()
				if errHijack != nil {
					t.Error(errHijack)
					return
				}
				defer func() { _ = client.Close() }()
				_, _ = fmt.Fprint(buffered, "HTTP/1.1 200 Connection Established\r\n\r\n")
				_ = buffered.Flush()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(remote, buffered); _ = remote.Close(); close(done) }()
				_, _ = io.Copy(client, remote)
				_ = client.Close()
				<-done
			}))
			defer selected.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fallbackHits.Add(1); w.WriteHeader(502) }))
			defer fallback.Close()
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}, Codex: config.CodexConfig{ResponseSteering: true}}
			cfg.Codex.CredentialIdentity.Enabled = true
			cfg.Codex.CredentialProxyPolicy = "require"
			cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"audit.-1": "once847", "prompt_cache_key": "configured847", "service_tier": "priority"}}}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			exec := NewCodexWebsocketsExecutor(cfg)
			exec.store = &codexWebsocketSessionStore{}
			manager.RegisterExecutor(exec)
			credential, errRegister := manager.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: "codex", Status: coreauth.StatusActive, ProxyURL: selected.URL,
				Attributes: map[string]string{"base_url": "http://loss847.invalid"}, Metadata: map[string]any{"access_token": "synthetic-loss847", "expired": "2100-01-01T00:00:00Z", "concurrency_limit": 2, "codex_identity_version": 1, "codex_identity_namespace": "a5bed5f1-a46c-408a-9c2c-7ba5a77f190e"}})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(credential.ID)
			input := make(chan core.WebsocketInput, 4)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			result, errExecute := manager.ExecuteStream(ctx, []string{"codex"}, core.Request{Model: model, Payload: []byte(`{"model":"gpt-5.4","event_id":"initial847","input":[]}`)}, core.Options{SourceFormat: translator.FormatOpenAIResponse})
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			readEvent := func(kind string) {
				t.Helper()
				select {
				case chunk := <-result.Chunks:
					if chunk.Err != nil || gjson.GetBytes(chunk.Payload, "type").String() != kind {
						t.Fatalf("event=%s err=%v, want %s", chunk.Payload, chunk.Err, kind)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("missing %s", kind)
				}
			}
			readEvent("response.created")
			initial := <-frames
			if gjson.GetBytes(initial, "audit").Raw != `["once847"]` || gjson.GetBytes(initial, "service_tier").String() != "priority" || gjson.GetBytes(initial, "prompt_cache_key").String() == "configured847" {
				t.Fatalf("final payload/identity boundary failed: %s", initial)
			}
			if phase == "pending_create" {
				input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","event_id":"follow847","input":[]}`)}
			} else {
				input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","event_id":"follow847","previous_response_id":"r847","input":"adjust"}`)}
			}
			select {
			case <-frames:
			case <-time.After(5 * time.Second):
				t.Fatal("follow-up did not reach selected socket")
			}
			wantActive := 2
			if phase != "pending_create" {
				readEvent("response.steer.accepted")
				if phase == "successor" {
					readEvent("response.completed")
					readEvent("response.created")
					wantActive = 1
				}
			}
			if active, limit := manager.CredentialConcurrencySnapshot(credential.ID); active != wantActive || limit != 2 {
				t.Fatalf("reserved capacity=%d/%d, want %d/2", active, limit, wantActive)
			}
			close(lose)
			var lost *core.UpstreamWebsocketTurnError
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					errors.As(chunk.Err, &lost)
				}
			}
			if lost == nil {
				t.Fatal("transport loss was not scoped to a recoverable turn")
			}
			found := false
			for _, abandoned := range lost.Abandoned {
				if abandoned.EventID == "follow847" && !abandoned.NotSent {
					found = true
				}
			}
			if !found {
				t.Fatalf("sent operation lost its correlation: %+v", lost)
			}
			cleanupCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			for {
				active, _ := manager.CredentialConcurrencySnapshot(credential.ID)
				if active == 0 {
					break
				}
				select {
				case <-cleanupCtx.Done():
					t.Fatalf("transport loss leaked %d slots", active)
				default:
					runtime.Gosched()
				}
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), credential)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			release()
			if selectedHits.Load() != 1 || upstreamHits.Load() != 1 || fallbackHits.Load() != 0 {
				t.Fatalf("loss replayed or changed route: selected=%d upstream=%d fallback=%d", selectedHits.Load(), upstreamHits.Load(), fallbackHits.Load())
			}
			current, _ := manager.GetByID(credential.ID)
			if current.Unavailable || current.Quota.Exceeded {
				t.Fatalf("transport loss cooled the credential: %+v", current.LastError)
			}
		})
	}
}
