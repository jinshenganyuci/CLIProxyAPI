package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestAntigravityNativeTerminationRetainsObservedUsageUpgrade815(t *testing.T) {
	for _, mode := range []string{"terminal_cancel", "partial_cancel", "backend_error", "multiline_error", "multiline_usage_error"} {
		t.Run(mode, func(t *testing.T) {
			capture := &providerV810UsageCapture{authID: t.Name(), records: make(chan usage.Record, 8)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
			finish := ""
			if mode == "terminal_cancel" {
				finish = `,"finishReason":"STOP"`
			}
			payload := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"observed"}]}` + finish + `}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}}`
			frame := "data: " + payload + "\n\n"
			if mode == "multiline_usage_error" {
				frame = "data: {\n\"response\":{\n\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"observed\"}]}}],\n\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":22,\"totalTokenCount\":33}\n}\n}\n\n"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, frame)
				w.(http.Flusher).Flush()
				if strings.HasSuffix(mode, "cancel") {
					<-r.Context().Done()
					return
				}
				if mode == "backend_error" {
					_, _ = io.WriteString(w, `data: {"error":{"code":503,"message":"synthetic backend failure"}}`+"\n\n")
				} else {
					_, _ = io.WriteString(w, "data: {\n\"error\": {\n\"code\": 503,\n\"message\": \"synthetic backend failure\"\n}\n}\n\n")
				}
			}))
			defer server.Close()
			base, executor, auth := issue6258Executor(t, "antigravity", server.URL)
			deliveryContext, finishDelivery := usage.WithStreamDelivery(base)
			ctx, cancel := context.WithTimeout(deliveryContext, 5*time.Second)
			defer cancel()
			errCount := 0
			observedTerminal := false
			for chunk := range issue6258StartStream(t, ctx, executor, auth, `{"input":"hello","stream":true}`) {
				if chunk.Err != nil {
					errCount++
				}
				text := string(chunk.Payload)
				if strings.Contains(text, `"response.completed"`) {
					observedTerminal = true
				}
				if mode == "terminal_cancel" && observedTerminal {
					finishDelivery(context.Canceled)
					cancel()
				}
				if mode == "partial_cancel" && strings.Contains(text, "observed") {
					finishDelivery(context.Canceled)
					cancel()
				}
			}
			finishDelivery(nil)
			if mode == "terminal_cancel" && !observedTerminal {
				t.Fatal("terminal cancellation never saw completion")
			}
			if mode != "terminal_cancel" && observedTerminal {
				t.Fatal("failed stream fabricated a completion")
			}
			if !strings.HasSuffix(mode, "cancel") && errCount != 1 {
				t.Fatalf("backend errors=%d, want 1", errCount)
			}
			records := antigravityUsageRecordsAfterDelivery815(t, capture)
			if len(records) != 1 {
				t.Fatalf("usage records=%d, want exactly one", len(records))
			}
			r := records[0]
			if r.Failed != (mode != "terminal_cancel") || r.Detail.InputTokens != 11 || r.Detail.OutputTokens != 22 || r.Detail.TotalTokens != 33 {
				t.Fatalf("termination usage failed=%t detail=%+v", r.Failed, r.Detail)
			}
			if !strings.HasSuffix(mode, "cancel") && r.Fail.StatusCode != 503 {
				t.Fatalf("backend status=%d, want 503", r.Fail.StatusCode)
			}
		})
	}
}

// Native delivery settlement publishes after closing the executor output channel.
func antigravityUsageRecordsAfterDelivery815(t *testing.T, capture *providerV810UsageCapture) []usage.Record {
	t.Helper()
	select {
	case first := <-capture.records:
		return append([]usage.Record{first}, providerV810UsageRecords(t, capture)...)
	case <-time.After(5 * time.Second):
		t.Fatal("delivery settlement did not publish usage")
		return nil
	}
}
