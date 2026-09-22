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

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutionProxyKeepsCredentialAuthoritative(t *testing.T) {
	for _, mode := range []string{"http", "http_stream", "compact", "websocket", "websocket_stream"} {
		for _, route := range []string{"matching", "conflicting", "direct_conflict", "unbound_override", "unbound_direct", "invalid_override"} {
			t.Run(mode+"/"+route, func(t *testing.T) {
				var originHits, selectedHits, requestHits, globalHits atomic.Int32
				const complete = `{"type":"response.completed","response":{"id":"proxy-test-response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
				respond := func(w http.ResponseWriter, r *http.Request) {
					if websocket.IsWebSocketUpgrade(r) {
						conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if errUpgrade != nil {
							t.Error(errUpgrade)
							return
						}
						defer func() { _ = conn.Close() }()
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							t.Error(errRead)
							return
						}
						_ = conn.WriteMessage(websocket.TextMessage, []byte(complete))
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					if strings.HasSuffix(r.URL.Path, "/compact") {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+complete+"\n\n")
				}
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { originHits.Add(1); respond(w, r) }))
				defer origin.Close()
				proxy := func(hits *atomic.Int32) *httptest.Server {
					return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						hits.Add(1)
						if r.Method != http.MethodConnect {
							respond(w, r)
							return
						}
						remote, errDial := net.Dial("tcp", strings.TrimPrefix(origin.URL, "http://"))
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
				}
				selected, requested, global := proxy(&selectedHits), proxy(&requestHits), proxy(&globalHits)
				defer selected.Close()
				defer requested.Close()
				defer global.Close()
				cfg := &config.Config{}
				cfg.ProxyURL = global.URL
				credential := &coreauth.Auth{ID: "proxy-upgrade-oauth", Provider: "codex", ProxyURL: selected.URL, Attributes: map[string]string{"base_url": origin.URL}, Metadata: map[string]any{"access_token": "synthetic-oauth"}}
				requestProxy := selected.URL
				wantFailure := false
				switch route {
				case "conflicting":
					requestProxy, wantFailure = requested.URL, true
				case "direct_conflict":
					credential.ProxyURL, requestProxy, wantFailure = "direct", requested.URL, true
				case "unbound_override":
					credential.ProxyURL, requestProxy, cfg.ProxyURL = "", requested.URL, "invalid-global-unused"
				case "unbound_direct":
					credential.ProxyURL, requestProxy = "", "direct"
				case "invalid_override":
					requestProxy, wantFailure = "http://user:secret@%zz", true
				}
				ctx := coreexecutor.WithRequestProxyURL(t.Context(), requestProxy)
				req := coreexecutor.Request{Model: "gpt-test", Payload: []byte(`{"model":"gpt-test","input":[]}`)}
				opts := coreexecutor.Options{SourceFormat: translator.FormatOpenAIResponse}
				executor := NewCodexExecutor(cfg)
				ws := NewCodexWebsocketsExecutor(cfg)
				ws.store = &codexWebsocketSessionStore{}
				var errExecute error
				switch mode {
				case "http":
					_, errExecute = executor.Execute(ctx, credential, req, opts)
				case "compact":
					opts.Alt = "responses/compact"
					_, errExecute = executor.Execute(ctx, credential, req, opts)
				case "websocket":
					_, errExecute = ws.Execute(ctx, credential, req, opts)
				default:
					var stream *coreexecutor.StreamResult
					opts.Stream = true
					if mode == "websocket_stream" {
						stream, errExecute = ws.ExecuteStream(ctx, credential, req, opts)
					} else {
						stream, errExecute = executor.ExecuteStream(ctx, credential, req, opts)
					}
					if stream != nil {
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								errExecute = chunk.Err
							}
						}
					}
				}
				if (errExecute != nil) != wantFailure {
					t.Fatalf("error=%v want_failure=%t", errExecute, wantFailure)
				}
				if wantFailure {
					if statusCodeFromTestError(t, errExecute) != 400 || strings.Contains(errExecute.Error(), "secret") || originHits.Load()+selectedHits.Load()+requestHits.Load()+globalHits.Load() != 0 {
						t.Fatalf("invalid override contacted an alternate route or lost error: err=%v origin=%d selected=%d request=%d global=%d", errExecute, originHits.Load(), selectedHits.Load(), requestHits.Load(), globalHits.Load())
					}
					return
				}
				if globalHits.Load() != 0 || (route == "matching" && (selectedHits.Load() != 1 || requestHits.Load() != 0)) || (route == "unbound_override" && (selectedHits.Load() != 0 || requestHits.Load() != 1)) || (route == "unbound_direct" && (originHits.Load() != 1 || selectedHits.Load()+requestHits.Load() != 0)) {
					t.Fatalf("wrong execution route: origin=%d selected=%d request=%d global=%d", originHits.Load(), selectedHits.Load(), requestHits.Load(), globalHits.Load())
				}
			})
		}
	}
}

func TestCodexExecutionConnectionKeyIncludesRequestProxyAndNamespace(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{CredentialIdentity: config.CodexCredentialIdentityConfig{Enabled: true}}}
	credential := &coreauth.Auth{ID: "identity", Metadata: map[string]any{"codex_identity_namespace": "namespace-a", "codex_identity_version": 1}}
	first, errFirst := helps.CodexWebsocketExecutionConnectionKey(coreexecutor.WithRequestProxyURL(context.Background(), "http://first.example:8080"), cfg, credential)
	second, errSecond := helps.CodexWebsocketExecutionConnectionKey(coreexecutor.WithRequestProxyURL(context.Background(), "http://second.example:8080"), cfg, credential)
	if errFirst != nil || errSecond != nil || first == second {
		t.Fatalf("distinct execution routes shared key: %v %v", errFirst, errSecond)
	}
	credential.Metadata["codex_identity_namespace"] = "namespace-b"
	third, errThird := helps.CodexWebsocketExecutionConnectionKey(coreexecutor.WithRequestProxyURL(context.Background(), "http://second.example:8080"), cfg, credential)
	if errThird != nil || third == second {
		t.Fatal("distinct credential namespaces shared connection key")
	}
	credential.ProxyURL = "direct"
	if _, errConflict := helps.CodexWebsocketExecutionConnectionKey(coreexecutor.WithRequestProxyURL(context.Background(), "http://second.example:8080"), cfg, credential); errConflict == nil {
		t.Fatal("conflicting request proxy admitted a retained connection")
	}
}

func TestCodexExecutionProxyOverrideDoesNotReplaceRequiredCredentialSetting(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: config.CodexCredentialProxyPolicyRequire}}
	ctx := coreexecutor.WithRequestProxyURL(t.Context(), "http://request.example:8080")
	credential := &coreauth.Auth{ID: "required-oauth", Provider: "codex", Metadata: map[string]any{"access_token": "synthetic"}}
	if errProxy := validateCodexCredentialProxyPolicy(cfg, credential, ctx); errProxy == nil || !strings.Contains(errProxy.Error(), "required") {
		t.Fatalf("request override bypassed the OAuth credential requirement: %v", errProxy)
	}
	credential.Attributes = map[string]string{"api_key": "synthetic"}
	if errProxy := validateCodexCredentialProxyPolicy(cfg, credential, ctx); errProxy != nil {
		t.Fatalf("OAuth require policy was expanded to API keys: %v", errProxy)
	}
}
