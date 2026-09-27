package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	usage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexDuplexUsageTracksResponseIdentityAndCancellation(t *testing.T) {
	for _, scenario := range []string{"active_cancel", "idle_close", "pending_quota_rejection"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			const firstCreated = `{"type":"response.created","response":{"id":"r1","model":"model-first"}}`
			const firstCompleted = `{"type":"response.completed","response":{"id":"r1","model":"model-first","output":[],"usage":{"input_tokens":4,"output_tokens":6,"total_tokens":10}}}`
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Error(errRead)
					return
				}
				write := func(payload string) {
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
						t.Error(errWrite)
					}
				}
				write(firstCreated)
				write(firstCompleted)
				write(firstCreated)
				write(firstCompleted)
				if scenario == "idle_close" {
					write(`{"type":"response.in_progress","response":{"id":"r1"}}`)
					_, _, _ = conn.ReadMessage()
					return
				}
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Error(errRead)
					return
				}
				if scenario == "pending_quota_rejection" {
					write(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"quota exhausted"}}`)
				} else {
					write(`{"type":"response.created","response":{"id":"r2","model":"model-second"}}`)
					write(firstCompleted)
					write(`{"type":"response.in_progress","response":{"id":"r2"}}`)
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer func() { cancel(); upstream.Close() }()
			alias := t.Name()
			capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan usage.Record, 16)}
			usage.RegisterNamedPlugin(alias, capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(alias, codexResponseModelNoopUsagePlugin{}) })
			input := make(chan core.WebsocketInput, 4)
			ctx = usage.WithRequestedModelAlias(core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input), alias)
			exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
			exec.store = &codexWebsocketSessionStore{}
			credential := &auth.Auth{ID: alias, Provider: "codex", ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": upstream.URL}}
			stream, errExecute := exec.ExecuteStream(ctx, credential, core.Request{Model: "requested-model", Payload: []byte(`{"model":"requested-model","input":[]}`)}, core.Options{SourceFormat: translator.FormatOpenAIResponse})
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			sent := false
			for chunk := range stream.Chunks {
				kind := gjson.GetBytes(chunk.Payload, "type").String()
				if kind == "response.completed" && !sent && scenario != "idle_close" {
					input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","input":[]}`)}
					sent = true
				}
				if kind == "response.in_progress" {
					cancel()
				}
			}
			// The usage queue is ordered: this marker makes absence of extra records
			// deterministic without a sleep or a timing-based negative assertion.
			usage.PublishRecord(context.Background(), usage.Record{Alias: alias, Model: "barrier"})
			var records []usage.Record
			for {
				record := capture.await(t)
				if record.Model == "barrier" {
					break
				}
				records = append(records, record)
			}
			want := 2
			if scenario == "idle_close" {
				want = 1
			}
			if len(records) != want {
				t.Fatalf("records=%+v, want %d (no duplicate created/terminal charges)", records, want)
			}
			if records[0].Failed || records[0].Detail.TotalTokens != 10 || records[0].ResponseModel != "model-first" {
				t.Fatalf("first response accounting changed: %+v", records[0])
			}
			if want == 2 && !records[1].Failed {
				t.Fatalf("second response lost its failure: %+v", records[1])
			}
			if scenario == "active_cancel" && records[1].ResponseModel != "model-second" {
				t.Fatalf("late first response contaminated second: %+v", records[1])
			}
			if scenario == "pending_quota_rejection" && (records[1].Fail.StatusCode != 429 || records[1].ResponseModel != "") {
				t.Fatalf("pending request quota rejection lost or inherited old response model: %+v", records[1])
			}
		})
	}
}
