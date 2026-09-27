package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

type devinUsageUpgrade738Capture struct {
	authID  string
	records chan usage.Record
}

func (p *devinUsageUpgrade738Capture) HandleUsage(_ context.Context, record usage.Record) {
	if record.Provider != "devin" || record.AuthID != p.authID {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}
func devinUsageUpgrade738CaptureFor(t *testing.T, authID string) <-chan usage.Record {
	t.Helper()
	name := "devin-upgrade738-" + uuid.NewString()
	capture := &devinUsageUpgrade738Capture{authID: authID, records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin(name, capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(name, &devinUsageUpgrade738Capture{}) })
	return capture.records
}
func devinUpgrade738Frames() []byte {
	var result bytes.Buffer
	appendField := func(n protowire.Number, value []byte) {
		frame := protowire.AppendTag(nil, n, protowire.BytesType)
		frame = protowire.AppendBytes(frame, value)
		result.Write(helps.WrapConnectEnvelope(frame))
	}
	var tokens []byte
	for _, pair := range [][2]int64{{2, 10}, {3, 5}, {4, 7}, {5, 3}} {
		tokens = protowire.AppendTag(tokens, protowire.Number(pair[0]), protowire.VarintType)
		tokens = protowire.AppendVarint(tokens, uint64(pair[1]))
	}
	tokens = protowire.AppendTag(tokens, 9, protowire.BytesType)
	tokens = protowire.AppendString(tokens, "swe-2-high")
	appendField(7, tokens)
	tool := protowire.AppendTag(nil, 1, protowire.BytesType)
	tool = protowire.AppendString(tool, "upgrade738-call")
	tool = protowire.AppendTag(tool, 2, protowire.BytesType)
	tool = protowire.AppendString(tool, "inspect")
	tool = protowire.AppendTag(tool, 3, protowire.BytesType)
	tool = protowire.AppendString(tool, `{`)
	appendField(6, tool)
	appendField(3, []byte("buffered answer 一"))
	continuation := protowire.AppendTag(nil, 1, protowire.BytesType)
	continuation = protowire.AppendString(continuation, "upgrade738-call")
	continuation = protowire.AppendTag(continuation, 3, protowire.BytesType)
	continuation = protowire.AppendString(continuation, `"done":true}`)
	appendField(6, continuation)
	return result.Bytes()
}
func TestDevinUpgrade738BufferedTextTerminationAndCapacity(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatInteractions, sdktranslator.FormatOpenAIResponse} {
		for _, ending := range []string{"eos", "trailer_error", "truncated", "read_error", "cancel"} {
			t.Run(fmt.Sprintf("%s/%s", format, ending), func(t *testing.T) {
				authID := "devin-buffer738-" + uuid.NewString()
				capture := devinUsageUpgrade738CaptureFor(t, authID)
				stopped := make(chan struct{})
				abortServer := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(stopped)
					w.Header().Set("Content-Type", "application/connect+proto")
					_, _ = w.Write(devinUpgrade738Frames())
					switch ending {
					case "eos":
						_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
					case "trailer_error":
						_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{"error":{"code":"invalid_argument","message":"synthetic failure"}}`)))
					case "read_error":
						_, _ = w.Write([]byte{0, 0})
					}
					w.(http.Flusher).Flush()
					if ending == "eos" || ending == "cancel" {
						select {
						case <-r.Context().Done():
						case <-abortServer:
						}
					}
				}))
				defer func() { close(abortServer); server.Close() }()
				manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewDevinExecutor(&config.Config{}))
				auth := &cliproxyauth.Auth{ID: authID, Provider: "devin", Status: cliproxyauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": server.URL}, Metadata: map[string]any{"concurrency_limit": 1}}
				registered, errRegister := manager.Register(context.Background(), auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(authID, "devin", []*registry.ModelInfo{{ID: "devin/swe-2"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				stream, errExecute := manager.ExecuteStream(ctx, []string{"devin"}, cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(`{"input":[{"type":"user_input","content":"hello"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions, ResponseFormat: format, Stream: true})
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				var text strings.Builder
				toolStopped, completed, failed, terminalErr := false, false, false, false
				cancellationTriggered := false
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						terminalErr = true
						continue
					}
					for _, line := range strings.Split(string(chunk.Payload), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						event := gjson.Parse(strings.TrimPrefix(line, "data: "))
						kind := event.Get("event_type").String()
						if kind == "" {
							kind = event.Get("type").String()
						}
						if (kind == "step.delta" && event.Get("delta.type").String() == "arguments_delta" && strings.Contains(event.Get("delta.arguments").String(), "done")) || (kind == "response.function_call_arguments.delta" && strings.Contains(event.Get("delta").String(), "done")) {
							if ending == "cancel" && !cancellationTriggered {
								if n, limit := manager.CredentialConcurrencySnapshot(authID); n != 1 || limit != 1 {
									t.Fatalf("active capacity=%d/%d", n, limit)
								}
								cancellationTriggered = true
								cancel()
							}
						}
						if (kind == "step.stop" && event.Get("index").Int() == 0) || (kind == "response.output_item.done" && event.Get("item.type").String() == "function_call") {
							toolStopped = true
						}
						if kind == "step.delta" && event.Get("delta.type").String() == "text" {
							if !toolStopped {
								t.Error("buffered text arrived before tool stopped")
							}
							text.WriteString(event.Get("delta.text").String())
						}
						if kind == "response.output_text.delta" {
							if !toolStopped {
								t.Error("buffered text arrived before tool stopped")
							}
							text.WriteString(event.Get("delta").String())
						}
						if kind == "interaction.completed" || kind == "response.completed" {
							completed = true
						}
						if kind == "response.failed" {
							failed = true
						}
					}
				}
				if ending == "cancel" {
					if !cancellationTriggered {
						t.Fatal("cancel did not occur after buffered text was received upstream")
					}
					if completed {
						t.Fatal("canceled stream fabricated completion")
					}
				} else {
					if ctx.Err() != nil {
						t.Fatalf("stream termination required timeout: %v", ctx.Err())
					}
					if text.String() != "buffered answer 一" {
						t.Fatalf("buffered text=%q", text.String())
					}
					if ending == "eos" {
						if !completed || terminalErr || failed {
							t.Fatalf("bad success completion=%t err=%t fail=%t", completed, terminalErr, failed)
						}
					} else if !terminalErr || completed {
						t.Fatalf("bad failure completion=%t err=%t", completed, terminalErr)
					}
				}
				select {
				case <-stopped:
				case <-time.After(time.Second):
					t.Fatal("upstream body was not closed")
				}
				if n, limit := manager.CredentialConcurrencySnapshot(authID); n != 0 || limit != 1 {
					t.Fatalf("leaked capacity=%d/%d", n, limit)
				}
				release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), registered)
				if errAcquire != nil {
					t.Fatalf("released slot unavailable: %v", errAcquire)
				}
				release()
				select {
				case record := <-capture:
					if record.Failed != (ending != "eos") {
						t.Errorf("usage Failed=%t, ending=%s", record.Failed, ending)
					}
					if record.ResponseModel != "swe-2-high" {
						t.Errorf("response model=%q", record.ResponseModel)
					}
					if ending == "eos" && (record.Detail.InputTokens != 13 || record.Detail.OutputTokens != 5 || record.Detail.CacheCreationTokens != 7) {
						t.Errorf("usage=%+v", record.Detail)
					}
				case <-time.After(time.Second):
					t.Fatal("missing usage record")
				}
			})
		}
	}
}

func TestDevinUpgrade738NonStreamFailureRetainsResponseModel(t *testing.T) {
	for _, ending := range []string{"truncated", "trailer_error"} {
		t.Run(ending, func(t *testing.T) {
			authID := "devin-model738-" + uuid.NewString()
			capture := devinUsageUpgrade738CaptureFor(t, authID)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/connect+proto")
				_, _ = w.Write(devinUpgrade738Frames())
				if ending == "trailer_error" {
					_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{"error":{"code":"invalid_argument","message":"synthetic failure"}}`)))
				}
			}))
			defer server.Close()
			exec := NewDevinExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: authID, Provider: "devin", ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic", "base_url": server.URL}}
			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(`{"input":[{"type":"user_input","content":"hello"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions})
			if err == nil {
				t.Fatal("expected upstream failure")
			}
			select {
			case record := <-capture:
				if !record.Failed || record.ResponseModel != "swe-2-high" {
					t.Fatalf("failure record Failed=%t response model=%q", record.Failed, record.ResponseModel)
				}
			case <-time.After(time.Second):
				t.Fatal("missing failure usage")
			}
		})
	}
}
