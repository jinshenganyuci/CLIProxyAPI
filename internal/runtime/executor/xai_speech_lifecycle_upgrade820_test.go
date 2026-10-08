package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAISpeechManagerProxyUsageAndCapacityUpgrade820(t *testing.T) {
	for _, mode := range []string{"direct", "proxy", "proxy-failure", "invalid-proxy", "request-override-keeps-credential", "cancel", "voice-invalid"} {
		t.Run(mode, func(t *testing.T) {
			var originCalls, proxyCalls, globalCalls atomic.Int32
			entered := make(chan struct{})
			releaseBody := make(chan struct{})
			closed := make(chan struct{})
			audio := []byte{'I', 'D', '3', 0, 255, 1, 2}
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originCalls.Add(1)
				defer close(closed)
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/v1/tts" || r.Header.Get("Authorization") != "Bearer synthetic-audio" || r.Header.Get("Accept") != "*/*" {
					t.Error("speech path or authentication headers changed")
				}
				if r.Header.Get(xaiClientVersionHeader) != "" || r.Header.Get(xaiTokenAuthHeader) != "" {
					t.Error("CLI chat headers leaked into speech")
				}
				if gjson.GetBytes(body, "voice_id").String() != "ara" || gjson.GetBytes(body, "language").Exists() {
					t.Errorf("payload rules were overwritten: %s", body)
				}
				if mode == "voice-invalid" {
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"error":"voice not found"}`)
					close(entered)
					return
				}
				w.Header().Set("Content-Type", "audio/mpeg")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(audio[:3])
				w.(http.Flusher).Flush()
				close(entered)
				select {
				case <-releaseBody:
					_, _ = w.Write(audio[3:])
				case <-r.Context().Done():
				}
			}))
			defer origin.Close()
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			defer transport.CloseIdleConnections()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyCalls.Add(1)
				if mode == "proxy-failure" {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				forward := r.Clone(r.Context())
				forward.RequestURI = ""
				resp, errDo := transport.RoundTrip(forward)
				if errDo != nil {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				defer func() { _ = resp.Body.Close() }()
				w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
				w.WriteHeader(resp.StatusCode)
				_, _ = io.Copy(w, resp.Body)
			}))
			defer proxy.Close()
			global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				globalCalls.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer global.Close()
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL}, Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "grok-tts", Protocol: "openai"}}, Params: map[string]any{"voice_id": "ara"}}},
				Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "grok-tts", Protocol: "openai"}}, Params: []string{"language"}}},
			}}
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewXAIExecutor(cfg))
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "xai", ProxyURL: "direct", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{"base_url": origin.URL + "/v1", "auth_kind": "oauth"},
				Metadata:   map[string]any{"access_token": "synthetic-audio", "concurrency_limit": 1},
			}
			if mode == "proxy" || mode == "proxy-failure" || mode == "request-override-keeps-credential" {
				auth.ProxyURL = proxy.URL
			} else if mode == "invalid-proxy" {
				auth.ProxyURL = "invalid://selected-proxy"
			}
			registered, errRegister := manager.Register(context.Background(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "xai", []*registry.ModelInfo{{ID: "grok-tts"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "request-override-keeps-credential" {
				ctx = cliproxyexecutor.WithRequestProxyURL(ctx, "direct")
			}
			type outcome struct {
				response cliproxyexecutor.Response
				err      error
			}
			done := make(chan outcome, 1)
			go func() {
				resp, errExecute := manager.Execute(ctx, []string{"xai"}, cliproxyexecutor.Request{Model: "grok-tts", Payload: []byte(`{"text":"hello","voice_id":"eve","language":"auto"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(xaiSpeechHandlerType)})
				done <- outcome{resp, errExecute}
			}()
			if mode == "direct" || mode == "proxy" || mode == "request-override-keeps-credential" || mode == "cancel" {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("origin did not receive speech request")
				}
				if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 1 || limit != 1 {
					t.Fatalf("speech body in progress uses %d/%d slots", active, limit)
				}
				if release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), registered); errAcquire == nil {
					release()
					t.Fatal("second request acquired occupied credential slot")
				}
				if mode == "cancel" {
					cancel()
				} else {
					close(releaseBody)
				}
			}
			var got outcome
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("speech request did not settle")
			}
			wantFailed := mode != "direct" && mode != "proxy" && mode != "request-override-keeps-credential"
			if (got.err != nil) != wantFailed {
				t.Fatalf("speech error=%v, want failed=%t", got.err, wantFailed)
			}
			if !wantFailed && (!bytes.Equal(got.response.Payload, audio) || got.response.Headers.Get("Content-Type") != "audio/mpeg") {
				t.Fatal("binary audio payload or content type changed")
			}
			wantOrigin, wantProxy := int32(1), int32(0)
			if mode == "proxy" || mode == "proxy-failure" || mode == "request-override-keeps-credential" {
				wantProxy = 1
			}
			if mode == "proxy-failure" || mode == "invalid-proxy" {
				wantOrigin = 0
			}
			if originCalls.Load() != wantOrigin || proxyCalls.Load() != wantProxy || globalCalls.Load() != 0 {
				t.Fatalf("unexpected route/retry: origin=%d proxy=%d global=%d", originCalls.Load(), proxyCalls.Load(), globalCalls.Load())
			}
			if active, limit := manager.CredentialConcurrencySnapshot(auth.ID); active != 0 || limit != 1 {
				t.Fatalf("speech completion leaked slot: %d/%d", active, limit)
			}
			release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), registered)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			release()
			records := providerV810UsageRecords(t, capture)
			if len(records) != 1 || records[0].Failed != wantFailed || records[0].Detail.TotalTokens != 0 {
				t.Fatalf("speech attempt usage must publish once without fabricated tokens: %+v", records)
			}
			if !wantFailed || mode == "voice-invalid" {
				current, _ := manager.GetByID(auth.ID)
				if current.Unavailable || !current.NextRetryAfter.IsZero() {
					t.Fatal("speech request fault/success cooled credential")
				}
			}
		})
	}
}
