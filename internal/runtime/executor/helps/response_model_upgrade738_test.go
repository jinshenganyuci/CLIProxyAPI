package helps

import (
	"bytes"
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"strings"
	"testing"
)

func TestResponseModelUpgrade738ClaudeRejectsTruncatedFrames(t *testing.T) {
	for _, payload := range []string{
		`{"type":"message_start","message":{"model":"fabricated-model"}`,
		`{"type":"message","model":"fabricated-model"`,
		`{"type":"message_stop"`,
	} {
		t.Run(payload, func(t *testing.T) {
			reporter := NewUsageReporter(context.Background(), "claude", "requested-model", nil)
			reporter.ObserveResponseModel([]byte(payload))
			if got := reporter.ResponseModel(); got != "" || reporter.IsResponseModelFinal() {
				t.Fatalf("truncated frame recorded model=%q, final=%t", got, reporter.IsResponseModelFinal())
			}
			reporter.ObserveResponseModel([]byte(`{"type":"message_start","message":{"model":"actual-model"}}`))
			reporter.ObserveResponseModel([]byte(`{"type":"message_stop"}`))
			if got := reporter.ResponseModel(); got != "actual-model" || !reporter.IsResponseModelFinal() {
				t.Fatalf("valid following frame model=%q, final=%t", got, reporter.IsResponseModelFinal())
			}
		})
	}
}

func TestResponseModelUpgrade738SSEObservesCompleteBoundedEvents(t *testing.T) {
	for _, test := range []struct {
		name   string
		chunks []string
	}{
		{"invalid-multiline", []string{"data: {\"model\":\"fabricated-model\",\"status\":\"completed\"}\ndata: invalid-json\n\n"}},
		{"oversized-first-line-single-chunk", []string{"data: " + strings.Repeat("x", defaultMaxStreamModelBufferBound+1) + "\ndata: {\"model\":\"fabricated-model\",\"status\":\"completed\"}\n\n"}},
		{"oversized-first-line-split-chunk", []string{"data: " + strings.Repeat("x", defaultMaxStreamModelBufferBound+1), "\ndata: {\"model\":\"fabricated-model\",\"status\":\"completed\"}\n\n"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reporter := NewUsageReporter(context.Background(), "openai-compat", "requested-model", nil)
			observer := NewStreamResponseModelObserver(reporter)
			for _, chunk := range test.chunks {
				observer.Feed([]byte(chunk))
			}
			if got := reporter.ResponseModel(); got != "" || reporter.IsResponseModelFinal() {
				t.Errorf("invalid SSE event recorded model=%q, final=%t", got, reporter.IsResponseModelFinal())
			}
			observer.Feed([]byte("data: {\"model\":\"actual-model\",\"status\":\"completed\"}\n\n"))
			observer.Finish()
			if got := reporter.ResponseModel(); got != "actual-model" || !reporter.IsResponseModelFinal() {
				t.Fatalf("following valid SSE event model=%q, final=%t", got, reporter.IsResponseModelFinal())
			}
		})
	}
}

func TestResponseModelUpgrade738CodexCompactionObject(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		want    string
		final   bool
	}{
		{"actual", `{"object":"response.compaction","model":"served-model","usage":{"input_tokens":3}}`, "served-model", true},
		{"missing-model", `{"object":"response.compaction","usage":{"input_tokens":3}}`, "", true},
		{"numeric-model", `{"object":"response.compaction","model":123}`, "", true},
		{"oversized-model", `{"object":"response.compaction","model":"` + strings.Repeat("x", maxResponseModelLength+1) + `"}`, "", true},
		{"malformed", `{"object":"response.compaction","model":"served-model"`, "", false},
		{"unrelated-object", `{"object":"not-a-response","model":"served-model"}`, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reporter := NewUsageReporter(context.Background(), "codex", "client-alias", nil)
			reporter.ObserveResponseModel([]byte(test.payload))
			if got := reporter.ResponseModel(); got != test.want || reporter.IsResponseModelFinal() != test.final {
				t.Errorf("model=%q final=%t, want %q/%t", got, reporter.IsResponseModelFinal(), test.want, test.final)
			}
			record := reporter.buildRecord(usage.Detail{}, false)
			if record.Model != "client-alias" || record.ResponseModel != test.want {
				t.Errorf("record changed accounting model or lost response model: %#v", record)
			}
		})
	}
}

func TestResponseModelUpgrade738SSEEveryChunkBoundary(t *testing.T) {
	wire := []byte(": keepalive\r\nevent: image_generation\r\ndata: {\r\ndata: \"model\":\"served-model\",\r\ndata: \"status\":\"completed\"}\r\n\r\n")
	unchanged := append([]byte(nil), wire...)
	for split := 0; split <= len(wire); split++ {
		reporter := NewUsageReporter(context.Background(), "openai-compat", "client-alias", nil)
		observer := NewStreamResponseModelObserver(reporter)
		observer.Feed(wire[:split])
		observer.Feed(wire[split:])
		observer.Finish()
		observer.Feed([]byte("data: {\"model\":\"must-not-replace\",\"status\":\"completed\"}\n\n"))
		if got := reporter.ResponseModel(); got != "served-model" || !reporter.IsResponseModelFinal() {
			t.Fatalf("split %d: model=%q final=%t", split, got, reporter.IsResponseModelFinal())
		}
		if !bytes.Equal(wire, unchanged) {
			t.Fatalf("split %d mutated caller-owned stream bytes", split)
		}
	}
}

func TestResponseModelUpgrade738StreamBoundsAndFinish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reporter := NewUsageReporter(ctx, "openai-compat", "client-alias", nil)
	observer := NewStreamResponseModelObserver(reporter)
	for range 4096 {
		observer.Feed([]byte(strings.Repeat("x", 511)))
		if len(observer.buf) > defaultMaxStreamModelBufferBound || cap(observer.buf) > 2*defaultMaxStreamModelBufferBound || observer.frameBytes > defaultMaxStreamModelBufferBound || len(observer.frame) > defaultMaxLinesPerStreamEvent {
			t.Fatal("observer retained unbounded fragmented payload")
		}
	}
	observer.Feed([]byte("\n\ndata: {\"model\":\"served-model\",\"status\":\"completed\"}"))
	cancel()
	observer.Finish()
	observer.Finish()
	if got := reporter.ResponseModel(); got != "served-model" {
		t.Fatalf("finish after cancellation lost complete trailing event: %q", got)
	}
	if len(observer.buf) != 0 || len(observer.frame) != 0 || observer.frameBytes != 0 {
		t.Fatal("finished observer retained buffered stream contents")
	}
	record := reporter.buildRecord(usage.Detail{InputTokens: 3, OutputTokens: 2}, false)
	if record.Model != "client-alias" || record.ResponseModel != "served-model" || record.Detail.InputTokens != 3 || record.Detail.OutputTokens != 2 {
		t.Fatalf("observability altered accounting record: %#v", record)
	}
}
