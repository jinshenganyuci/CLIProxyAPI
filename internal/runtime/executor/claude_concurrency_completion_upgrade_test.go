package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type claudeCompletionUsageCapture struct {
	authID  string
	model   string
	records chan usage.Record
}

func (p *claudeCompletionUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.Provider != "claude" || record.AuthID != p.authID || record.Model != p.model {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func TestClaudeConcurrencyCompletionClosesHangingUpstreamAndReleasesCapacity(t *testing.T) {
	const streamData = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_capacity","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	for _, target := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse} {
		for _, cancelAtTerminal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-cancel-%t", target, cancelAtTerminal), func(t *testing.T) {
				authID := "claude-capacity-completion-" + uuid.NewString()
				upstreamClosed := make(chan struct{})
				stopUpstream := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(upstreamClosed)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(streamData))
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
					case <-stopUpstream:
					}
				}))
				defer func() { close(stopUpstream); server.Close() }()

				pluginName := "test-claude-capacity-completion-" + uuid.NewString()
				capture := &claudeCompletionUsageCapture{authID: authID, model: "claude-opus-5", records: make(chan usage.Record, 4)}
				usage.RegisterNamedPlugin(pluginName, capture)
				t.Cleanup(func() { usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{}) })
				// An earlier test can leave records pending in the shared asynchronous dispatcher.
				usage.PublishRecord(context.Background(), usage.Record{
					Provider: "claude", AuthID: "unrelated-completion-test-" + uuid.NewString(), Model: "claude-opus-5", Failed: true,
				})

				manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
				auth := &cliproxyauth.Auth{
					ID: authID, Provider: "claude", Status: cliproxyauth.StatusActive,
					Attributes: map[string]string{"api_key": "synthetic-key", "base_url": server.URL},
					Metadata:   map[string]any{"concurrency_limit": 1},
				}
				registered, errRegister := manager.Register(context.Background(), auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-opus-5"}})
				t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result, errExecute := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{
					Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: target, Stream: true})
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				terminal := "message_stop"
				if target == sdktranslator.FormatOpenAIResponse {
					terminal = "response.completed"
				}
				sawTerminal := false
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("completed stream emitted error: %v", chunk.Err)
					}
					if strings.Contains(string(chunk.Payload), terminal) {
						sawTerminal = true
						if cancelAtTerminal {
							cancel()
						}
					}
				}
				if !sawTerminal {
					t.Fatalf("missing terminal event %q", terminal)
				}
				if !cancelAtTerminal && ctx.Err() != nil {
					t.Fatalf("completion required downstream cancellation: %v", ctx.Err())
				}
				select {
				case <-upstreamClosed:
				case <-time.After(time.Second):
					t.Fatal("completed stream did not close hanging upstream body")
				}
				if inflight, limit := manager.CredentialConcurrencySnapshot(auth.ID); inflight != 0 || limit != 1 {
					t.Fatalf("capacity after terminal EOF = %d/%d, want 0/1", inflight, limit)
				}
				current, _ := manager.GetByID(auth.ID)
				if current.LastError != nil || current.Unavailable || current.Quota.Exceeded || !current.NextRetryAfter.IsZero() {
					t.Fatalf("successful completion poisoned credential health: %#v", current)
				}
				release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), registered)
				if errAcquire != nil {
					t.Fatalf("released capacity cannot be reacquired: %v", errAcquire)
				}
				release()
				select {
				case record := <-capture.records:
					if record.Failed || record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 {
						t.Fatalf("completed usage changed: auth=%q want-auth=%q model=%q failed=%t detail=%#v", record.AuthID, auth.ID, record.Model, record.Failed, record.Detail)
					}
				case <-time.After(time.Second):
					t.Fatal("completion did not publish usage")
				}
			})
		}
	}
}
