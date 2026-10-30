package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV848CapsuleStructuredUsagePreservesFinalPayloadIdentityAndProxy(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "ws-execute", "ws-stream", "duplex"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			const model = "gpt-6.1-sol"
			capsule, errSeal := helps.SealAntigravityCompaction("summary before user override", "claude-sonnet-4-6")
			if errSeal != nil {
				t.Fatal(errSeal)
			}
			request := []byte(fmt.Sprintf(`{"model":%q,"input":[{"type":"compaction","id":"cmp_ag_compact_848","encrypted_content":%q},{"type":"compaction","encrypted_content":"gAAAAABnative848"},{"role":"user","content":"literal cpa-ag-compact-v1:text"}],"prompt_cache_key":"client848"}`, model, capsule))
			original := bytes.Clone(request)
			credential := codexCredentialIdentityTestAuth(t.Name(), "ef49c63c-c044-433d-a85a-0ca341d4f7e9")
			credential.Provider = "codex"
			namespace, _, _ := codexauth.ParseCredentialIdentity(credential.Metadata)
			mapped := codexauth.DeriveCredentialIdentity(namespace, "session", "configured848")
			var selectedHits, fallbackHits, upstreamHits atomic.Int32
			inspect := func(body []byte) {
				t.Helper()
				if gjson.GetBytes(body, "input.0.type").String() != "message" || gjson.GetBytes(body, "input.0.role").String() != "developer" || gjson.GetBytes(body, "input.0.content.0.text").String() != "final848" {
					t.Errorf("capsule expansion/final barrier: %s", body)
				}
				if gjson.GetBytes(body, "input.1.encrypted_content").String() != "gAAAAABnative848" || gjson.GetBytes(body, "input.2.content").String() != "literal cpa-ag-compact-v1:text" {
					t.Errorf("opaque/literal modified: %s", body)
				}
				if gjson.GetBytes(body, "prompt_cache_key").String() != mapped || gjson.GetBytes(body, "audit").Raw != `["once848"]` || gjson.GetBytes(body, "service_tier").String() != "priority" {
					t.Errorf("identity/final rules repeated or lost: %s", body)
				}
			}
			events := func(turn int) []string {
				id := fmt.Sprintf("r848-%d", turn)
				item := `{"id":"web848","type":"web_search_call","status":"completed","action":{"type":"search","query":"synthetic"}}`
				if turn == 2 {
					item = `{"id":"fn848","type":"function_call","call_id":"call848","name":"weather","arguments":"{}"}`
				}
				terminal := "response.incomplete"
				if mode == "duplex" && turn == 1 {
					terminal = "response.completed"
				}
				return []string{fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"model":%q}}`, id, model), fmt.Sprintf(`{"type":"response.output_item.done","response_id":%q,"output_index":0,"item":%s}`, id, item), fmt.Sprintf(`{"type":%q,"response":{"id":%q,"model":%q,"status":%q,"output":[],"metadata":{"prompt_cache_key":%q},"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}`, terminal, id, model, strings.TrimPrefix(terminal, "response."), mapped)}
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits.Add(1)
				if websocket.IsWebSocketUpgrade(r) {
					conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Error(errUpgrade)
						return
					}
					defer func() { _ = conn.Close() }()
					turns := 1
					if mode == "duplex" {
						turns = 2
					}
					for turn := 1; turn <= turns; turn++ {
						_, body, errRead := conn.ReadMessage()
						if errRead != nil {
							t.Error(errRead)
							return
						}
						inspect(body)
						for _, event := range events(turn) {
							if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
								t.Error(errWrite)
								return
							}
						}
					}
					if mode == "duplex" {
						_, _, _ = conn.ReadMessage()
					}
					return
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
					return
				}
				inspect(body)
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range events(1) {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
				}
			}))
			defer func() { cancel(); upstream.Close() }()
			target, _ := url.Parse(upstream.URL)
			reverse := httputil.NewSingleHostReverseProxy(target)
			reverse.Transport = &http.Transport{Proxy: nil}
			defer reverse.Transport.(*http.Transport).CloseIdleConnections()
			selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				selectedHits.Add(1)
				if r.Method != http.MethodConnect {
					if r.URL.Host != "capsule848.invalid" {
						t.Errorf("proxy target=%s", r.URL)
					}
					reverse.ServeHTTP(w, r)
					return
				}
				if r.Host != "capsule848.invalid:80" {
					t.Errorf("CONNECT target=%s", r.Host)
				}
				remote, errDial := net.Dial("tcp", target.Host)
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
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbackHits.Add(1); w.WriteHeader(502) }))
			defer fallback.Close()
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}, Codex: config.CodexConfig{ResponseSteering: mode == "duplex"}}
			cfg.Codex.CredentialIdentity.Enabled = true
			cfg.Codex.CredentialProxyPolicy = "require"
			cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"input.0.content.0.text": "final848", "prompt_cache_key": "configured848", "audit.-1": "once848", "service_tier": "priority"}}}
			manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			if strings.HasPrefix(mode, "ws-") || mode == "duplex" {
				exec := NewCodexWebsocketsExecutor(cfg)
				exec.store = &codexWebsocketSessionStore{}
				manager.RegisterExecutor(exec)
			} else {
				manager.RegisterExecutor(NewCodexExecutor(cfg))
			}
			credential.ProxyURL = selected.URL
			credential.Attributes = map[string]string{"base_url": "http://capsule848.invalid"}
			registered, errRegister := manager.Register(ctx, credential)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(registered.ID, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(registered.ID)
			capture := &providerV810UsageCapture{authID: registered.ID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			defer usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{})
			req := core.Request{Model: model, Payload: request}
			opts := core.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: request}
			if mode == "execute" || mode == "ws-execute" {
				resp, errExec := manager.Execute(ctx, []string{"codex"}, req, opts)
				if errExec != nil {
					t.Fatal(errExec)
				}
				if !resp.Incomplete {
					t.Error("incomplete response lost status bit")
				}
				if gjson.GetBytes(resp.Payload, "metadata.prompt_cache_key").String() != "configured848" {
					t.Errorf("response identity not restored: %s", resp.Payload)
				}
			} else {
				input := make(chan core.WebsocketInput, 2)
				if mode == "duplex" {
					ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
				}
				opts.Stream = true
				result, errExec := manager.ExecuteStream(ctx, []string{"codex"}, req, opts)
				if errExec != nil {
					t.Fatal(errExec)
				}
				sawIncomplete := false
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					if chunk.Incomplete {
						sawIncomplete = true
						if mode == "duplex" {
							cancel()
						}
					}
					if mode == "duplex" && gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
						input <- core.WebsocketInput{Payload: append([]byte(`{"type":"response.create",`), request[1:]...)}
					}
				}
				if !sawIncomplete {
					t.Error("stream lost incomplete status bit")
				}
			}
			if !bytes.Equal(request, original) {
				t.Error("caller payload mutated")
			}
			records := providerV810UsageRecords(t, capture)
			want := 1
			if mode == "duplex" {
				want = 2
			}
			if len(records) != want {
				t.Fatalf("usage records=%+v", records)
			}
			for i, r := range records {
				if r.Failed || r.Detail.InputTokens != 10 || r.Detail.OutputTokens != 4 || r.Detail.CachedTokens != 3 || r.Detail.ReasoningTokens != 2 {
					t.Errorf("usage outcome/details lost: %+v", r)
				}
				d := r.Detail.ToolUsageDetails
				if i == 0 {
					if d == nil || d.WebSearchCalls == nil || *d.WebSearchCalls != 1 {
						t.Errorf("output_item.done search lost: %+v", d)
					}
				} else if d == nil || d.ToolCallsRequested == nil || *d.ToolCallsRequested != 1 || d.WebSearchCalls != nil {
					t.Errorf("response usage crossed turns: %+v", d)
				}
			}
			if active, _ := manager.CredentialConcurrencySnapshot(registered.ID); active != 0 {
				t.Errorf("leaked slots=%d", active)
			}
			if selectedHits.Load() != 1 || upstreamHits.Load() != 1 || fallbackHits.Load() != 0 {
				t.Errorf("route selected=%d upstream=%d fallback=%d", selectedHits.Load(), upstreamHits.Load(), fallbackHits.Load())
			}
		})
	}
}
