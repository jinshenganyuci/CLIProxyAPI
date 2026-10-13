package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexReloadRetainsGlobalRouteButRejectsCredentialChangesUpgrade827(t *testing.T) {
	for _, mode := range []string{"global-reload", "invalid-global-reload", "credential-proxy", "namespace", "identity-disabled", "require", "siwc-mode"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", mode, stream), func(t *testing.T) {
				var handshakes, frames, alternateCalls atomic.Int32
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Error(errUpgrade)
						return
					}
					handshakes.Add(1)
					defer func() { _ = conn.Close() }()
					for {
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							return
						}
						frames.Add(1)
						if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_reload","status":"completed","output":[],"usage":{"input_tokens":11,"output_tokens":22,"total_tokens":33}}}`)); errWrite != nil {
							return
						}
					}
				}))
				defer origin.Close()
				alternate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					alternateCalls.Add(1)
					w.WriteHeader(http.StatusBadGateway)
				}))
				defer alternate.Close()
				cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "direct"}}
				cfg.Codex.CredentialIdentity.Enabled = true
				auth := &coreauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"base_url": origin.URL, "websockets": "true"}, Metadata: map[string]any{"access_token": "synthetic-outer", "codex_identity_namespace": "c8775040-a5bc-4bc5-893c-bd631bacd584", "codex_identity_version": 1}}
				first := NewCodexAutoExecutor(cfg)
				first.wsExec.store = &codexWebsocketSessionStore{}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.RegisterExecutor(first)
				defer manager.CloseExecutionSession(t.Name())
				opts := core.Options{SourceFormat: translator.FormatOpenAIResponse, Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name()}}
				req := core.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":[],"prompt_cache_key":"reload-client"}`)}
				ctx := core.WithDownstreamWebsocket(context.Background())
				invoke := func(exec *CodexAutoExecutor, ctx context.Context, credential *coreauth.Auth) error {
					if !stream {
						_, errExecute := exec.Execute(ctx, credential, req, opts)
						return errExecute
					}
					result, errExecute := exec.ExecuteStream(ctx, credential, req, opts)
					if errExecute != nil {
						return errExecute
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errExecute = chunk.Err
						}
					}
					return errExecute
				}
				if errFirst := invoke(first, ctx, auth); errFirst != nil {
					t.Fatal(errFirst)
				}
				sess := first.wsExec.getOrCreateSession(t.Name())
				sess.connMu.Lock()
				originalConn := sess.conn
				sess.connMu.Unlock()
				nextCfg := *cfg
				nextCfg.ProxyURL = alternate.URL
				nextAuth := auth.Clone()
				switch mode {
				case "invalid-global-reload":
					nextCfg.ProxyURL = "invalid-unused-global"
				case "credential-proxy":
					nextAuth.ProxyURL = alternate.URL
				case "namespace":
					nextAuth.Metadata["codex_identity_namespace"] = "828d6341-93ab-43d3-84c5-d4d31058ca83"
				case "identity-disabled":
					nextCfg.Codex.CredentialIdentity.Enabled = false
				case "require":
					nextCfg.Codex.CredentialProxyPolicy = config.CodexCredentialProxyPolicyRequire
				case "siwc-mode":
					nextAuth.Metadata["use_siwc"] = true
					nextAuth.Metadata["siwc"] = map[string]any{"access_token": "synthetic-inner"}
				}
				next := NewCodexAutoExecutor(&nextCfg)
				next.wsExec.store = first.wsExec.store
				manager.RegisterExecutor(next)
				errNext := invoke(next, core.WithRequiredUpstreamWebsocket(ctx), nextAuth)
				wantSuccess := mode == "global-reload" || mode == "invalid-global-reload"
				if (errNext == nil) != wantSuccess {
					t.Fatalf("reload result=%v success=%t", errNext, wantSuccess)
				}
				if !wantSuccess && mode != "require" && !core.IsUpstreamWebsocketReplayRequired(errNext) {
					t.Fatalf("credential transition must reject retained transport: %v", errNext)
				}
				if mode == "require" && statusCodeFromTestError(t, errNext) != http.StatusServiceUnavailable {
					t.Fatalf("new proxy requirement was bypassed: %v", errNext)
				}
				wantFrames := int32(1)
				if wantSuccess {
					wantFrames++
				}
				sess.connMu.Lock()
				stillSameConn := sess.conn == originalConn
				sess.connMu.Unlock()
				if !stillSameConn || handshakes.Load() != 1 || frames.Load() != wantFrames || alternateCalls.Load() != 0 {
					t.Fatalf("session crossed boundary: same=%t handshakes=%d frames=%d alternate=%d", stillSameConn, handshakes.Load(), frames.Load(), alternateCalls.Load())
				}
			})
		}
	}
}
