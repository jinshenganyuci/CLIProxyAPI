package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const filterNotice856 = clienterror.GeminiFilterBlockMessagePrefix + " See " + clienterror.GeminiFilterBlockPolicyURL

func TestV856AntigravityFilterRetainsUsageAndCompletedTail(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, completed := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/completed=%t", stream, completed), func(t *testing.T) {
				const model = "claude-sonnet-4-6"
				var selectedHits, fallbackHits atomic.Int32
				var manager *coreauth.Manager
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					selectedHits.Add(1)
					if active, _ := manager.CredentialConcurrencySnapshot(t.Name()); active != 1 {
						t.Errorf("active slots = %d, want 1", active)
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil || ojson.GetBytes(body, "request.audit.calls.#").Int() != 1 || ojson.GetBytes(body, "request.audit.large").Raw != "9007199254740993" {
						t.Errorf("final payload changed: %s, err=%v", body, errRead)
					}
					finish := ""
					if completed {
						finish = `,"finishReason":"STOP"`
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"thought\":true,\"text\":\"thinking\"}]}%s}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":0,\"thoughtsTokenCount\":10,\"totalTokenCount\":110}}}\n\ndata: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":%q}]}}]}}\n\n", finish, filterNotice856)
				}))
				defer selected.Close()
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fallbackHits.Add(1)
					w.WriteHeader(http.StatusBadGateway)
				}))
				defer fallback.Close()
				match := []config.PayloadModelRule{{Name: "*"}}
				cfg := &config.Config{AuthDir: t.TempDir(), SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}, Payload: config.PayloadConfig{
					Override:    []config.PayloadRule{{Models: match, Params: map[string]any{"audit.calls.-1": "once"}}},
					OverrideRaw: []config.PayloadRule{{Models: match, Params: map[string]any{"audit.large": "9007199254740993"}}},
				}}
				manager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewAntigravityExecutor(cfg))
				auth := &coreauth.Auth{ID: t.Name(), Provider: "antigravity", ProxyURL: selected.URL, Attributes: map[string]string{"base_url": "http://antigravity856.invalid"}, Metadata: map[string]any{"access_token": "synthetic856", "project_id": "synthetic", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
				if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
				usage.RegisterNamedPlugin(auth.ID, capture)
				t.Cleanup(func() { usage.RegisterNamedPlugin(auth.ID, antigravityUsageNoop{}) })
				req := coreexecutor.Request{Model: model, Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"explain a quoted notice"}]}]}`)}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Stream: stream}
				var response []byte
				var errExecute error
				if stream {
					var result *coreexecutor.StreamResult
					result, errExecute = manager.ExecuteStream(t.Context(), []string{auth.Provider}, req, opts)
					if errExecute == nil {
						for chunk := range result.Chunks {
							response = append(response, chunk.Payload...)
							if chunk.Err != nil {
								errExecute = chunk.Err
							}
						}
					}
				} else {
					var result coreexecutor.Response
					result, errExecute = manager.Execute(t.Context(), []string{auth.Provider}, req, opts)
					response = result.Payload
				}
				if completed {
					if errExecute != nil || !bytes.Contains(response, []byte(filterNotice856)) {
						t.Errorf("completed quote lost or misclassified: err=%v body=%s", errExecute, response)
					}
				} else {
					var status interface{ StatusCode() int }
					if !errors.As(errExecute, &status) || status.StatusCode() != http.StatusBadRequest {
						t.Errorf("filter error = %v, want 400", errExecute)
					}
				}
				// Stream completion precedes deferred usage settlement. Wait for
				// publication before enqueuing the FIFO barrier, which only orders
				// records that have already reached the usage manager.
				var firstRecord usage.Record
				select {
				case firstRecord = <-capture.records:
				case <-time.After(5 * time.Second):
					t.Fatal("filter outcome did not settle usage")
				}
				records := append([]usage.Record{firstRecord}, providerV810UsageRecords(t, capture)...)
				if len(records) != 1 || records[0].Failed == completed || records[0].Detail.InputTokens != 100 || records[0].Detail.ReasoningTokens != 10 || records[0].Detail.TotalTokens != 110 {
					t.Errorf("filter accounting = %+v, want one outcome with consumed 100/10/110 tokens", records)
				}
				if active, _ := manager.CredentialConcurrencySnapshot(auth.ID); active != 0 || selectedHits.Load() != 1 || fallbackHits.Load() != 0 {
					t.Errorf("slots/selected/fallback = %d/%d/%d", active, selectedHits.Load(), fallbackHits.Load())
				}
			})
		}
	}
}

func TestV856PendingFilterCancellationPreservesObservedUsage(t *testing.T) {
	for _, provider := range []string{"gemini", "vertex"} {
		t.Run(provider, func(t *testing.T) {
			auth := &coreauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"api_key": "synthetic856", "base_url": "http://synthetic856.invalid"}}
			capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(auth.ID, capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(auth.ID, antigravityUsageNoop{}) })
			var exec coreauth.ProviderExecutor = NewGeminiExecutor(&config.Config{AuthDir: t.TempDir()})
			if provider == "vertex" {
				exec = NewGeminiVertexExecutor(&config.Config{AuthDir: t.TempDir()})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// Fill the output buffer, hold a possible notice, then cancel exactly
			// while the scanner reads the final usage frame. No consumer can free
			// the buffer before failure accounting completes.
			first := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"begin\"}]}}]}\n\n"
			pending := fmt.Sprintf("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":%q}]}}]}\n\n", filterNotice856)
			final := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"quoted\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":8,\"totalTokenCount\":20}}\n\n"
			ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				body := io.NopCloser(io.MultiReader(strings.NewReader(strings.TrimSuffix(first, "\n")), strings.NewReader(strings.TrimSuffix(pending, "\n")), &cancelBeforeRead856{Reader: strings.NewReader(final), cancel: cancel}))
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body, Request: r}, nil
			}))
			result, errStream := exec.ExecuteStream(ctx, auth, coreexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"quote"}]}]}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Stream: true})
			if errStream != nil {
				t.Fatal(errStream)
			}
			var firstRecord usage.Record
			select {
			case firstRecord = <-capture.records:
			case <-time.After(5 * time.Second):
				t.Fatal("canceled stream did not settle usage while output was blocked")
			}
			for range result.Chunks {
			}
			records := append([]usage.Record{firstRecord}, providerV810UsageRecords(t, capture)...)
			if len(records) != 1 || !records[0].Failed || records[0].Detail.InputTokens != 12 || records[0].Detail.OutputTokens != 8 || records[0].Detail.TotalTokens != 20 {
				t.Errorf("read-ahead usage lost on pending-frame cancellation: %+v", records)
			}
		})
	}
}

type cancelBeforeRead856 struct {
	io.Reader
	cancel context.CancelFunc
}

func TestV856AntigravityHeldNoticeDoesNotDeliverFutureTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":%q}]}}]}}\n\ndata: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"quoted\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":8,\"totalTokenCount\":20}}}\n\n", filterNotice856)
	}))
	defer server.Close()
	auth := &coreauth.Auth{ID: t.Name(), Provider: "antigravity", ProxyURL: "direct", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "synthetic856", "project_id": "synthetic", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
	capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
	usage.RegisterNamedPlugin(auth.ID, capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(auth.ID, antigravityUsageNoop{}) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := coreexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"quote"}]}]}`)}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Stream: true, Metadata: map[string]any{coreexecutor.ExecutionSessionMetadataKey: t.Name()}}
	result, errStream := NewAntigravityExecutor(&config.Config{AuthDir: t.TempDir()}).ExecuteStream(ctx, auth, req, opts)
	if errStream != nil {
		t.Fatal(errStream)
	}
	first, ok := <-result.Chunks
	if !ok || first.Err != nil || !bytes.Contains(first.Payload, []byte(filterNotice856)) || bytes.Contains(first.Payload, []byte("finishReason")) {
		t.Fatalf("first payload is not the held non-terminal quote: %+v", first)
	}
	cancel()
	// Leave the unbuffered output unread until settlement, so a later terminal
	// cannot be delivered after cancellation and mistaken for the first payload.
	var firstRecord usage.Record
	select {
	case firstRecord = <-capture.records:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled held-frame stream did not settle")
	}
	for range result.Chunks {
	}
	records := append([]usage.Record{firstRecord}, providerV810UsageRecords(t, capture)...)
	if len(records) != 1 || !records[0].Failed || records[0].Detail.InputTokens != 12 || records[0].Detail.OutputTokens != 8 {
		t.Fatalf("undelivered completion produced success or lost usage: %+v", records)
	}
}

func (r *cancelBeforeRead856) Read(p []byte) (int, error) {
	r.cancel()
	return r.Reader.Read(p)
}
