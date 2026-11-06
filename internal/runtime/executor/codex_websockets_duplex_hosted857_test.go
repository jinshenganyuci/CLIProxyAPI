package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestV858CodexDuplexExplicitHostedImageModelExpected(t *testing.T) {
	for _, servedModel := range []string{"gpt-5.4-mini", "unexpected-codex-model"} {
		t.Run(servedModel, func(t *testing.T) {
			originalHooks := cloneLogHooks(log.StandardLogger().Hooks)
			hook := new(logtest.Hook)
			log.StandardLogger().AddHook(hook)
			t.Cleanup(func() { log.StandardLogger().ReplaceHooks(originalHooks) })
			serverDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				// This deadline bounds a failing test, not production network behavior.
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				for turn := 1; turn <= 2; turn++ {
					_, body, errRead := conn.ReadMessage()
					if errRead != nil {
						t.Errorf("read turn %d: %v", turn, errRead)
						return
					}
					wantModel := "gpt-6-astra"
					responseModel := wantModel
					inputTokens, outputTokens := 2, 1
					if turn == 2 {
						wantModel, responseModel = "gpt-5.4-mini", servedModel
						inputTokens, outputTokens = 5, 7
						if ojson.GetBytes(body, "tools.0.type").String() != "image_generation" || ojson.GetBytes(body, "tools.0.complexity").Exists() {
							t.Errorf("hosted image tool was not normalized: %s", body)
						}
					}
					if ojson.GetBytes(body, "model").String() != wantModel {
						t.Errorf("turn %d upstream model = %q, want %q", turn, ojson.GetBytes(body, "model").String(), wantModel)
					}
					created := fmt.Sprintf(`{"type":"response.created","response":{"id":"r%d","model":%q,"output":[]}}`, turn, responseModel)
					completed := fmt.Sprintf(`{"type":"response.completed","response":{"id":"r%d","model":%q,"status":"completed","output":[],"usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}}}`, turn, responseModel, inputTokens, outputTokens, inputTokens+outputTokens)
					for _, event := range []string{created, completed} {
						if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
							t.Errorf("write turn %d: %v", turn, errWrite)
							return
						}
					}
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
			defer cancel()
			input := make(chan core.WebsocketInput, 1)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{ResponseSteering: true}}
			// Hosted image turns now require an explicit supported-model override.
			cfg.Payload.Override = []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-6-astra", Match: []map[string]any{{"tool_choice.type": "image_generation"}}}},
				Params: map[string]any{"model": "gpt-5.4-mini"},
			}}
			exec := NewCodexWebsocketsExecutor(cfg)
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", ProxyURL: "direct", Attributes: map[string]string{"base_url": server.URL, "websockets": "true", "plan_type": "pro"}, Metadata: map[string]any{"access_token": "synthetic-857", "account_id": "synthetic-account"}}
			capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(auth.ID, capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(auth.ID, antigravityUsageNoop{}) })
			req := core.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":"first","tools":[]}`)}
			opts := core.Options{SourceFormat: sdktranslator.FormatCodex, Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name()}}
			result, errStream := exec.ExecuteStream(ctx, auth, req, opts)
			if errStream != nil {
				t.Fatal(errStream)
			}
			completed := 0
			for completed < 2 {
				select {
				case chunk, ok := <-result.Chunks:
					if !ok || chunk.Err != nil {
						t.Fatalf("duplex ended before both responses: open=%t err=%v", ok, chunk.Err)
					}
					if ojson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
						completed++
						if completed == 1 {
							input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"gpt-6-astra","input":"draw","reasoning":{"effort":"medium"},"tools":[{"type":"image_generation","complexity":"high"}],"tool_choice":{"type":"image_generation"}}`)}
						}
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			cancel()
			for range result.Chunks {
			}
			<-serverDone
			records := providerV810UsageRecords(t, capture)
			if len(records) != 2 {
				t.Fatalf("usage records = %+v, want exactly two", records)
			}
			for index, record := range records {
				wantResponse, wantTotal := "gpt-6-astra", int64(3)
				if index == 1 {
					wantResponse, wantTotal = servedModel, 12
				}
				if record.Failed || record.Model != req.Model || record.ResponseModel != wantResponse || record.Detail.TotalTokens != wantTotal {
					t.Errorf("turn %d usage changed: %+v", index+1, record)
				}
			}
			warnings := 0
			for _, entry := range hook.AllEntries() {
				if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream served model") {
					warnings++
				}
			}
			wantWarnings := 0
			if servedModel != "gpt-5.4-mini" {
				wantWarnings = 1
			}
			if warnings != wantWarnings {
				t.Errorf("model substitution warnings = %d, want %d for served %q", warnings, wantWarnings, servedModel)
			}
		})
	}
}
