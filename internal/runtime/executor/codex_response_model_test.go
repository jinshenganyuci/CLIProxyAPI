package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const codexResponseModelTestStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-luna"}}

data: {"type":"response.output_text.delta","delta":"he"}

data: {"type":"response.output_text.delta","delta":"llo"}

data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-luna","usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}}

data: [DONE]`

// TestObserveCodexTokenEventRecordsResponseModel guards the wiring between the codex
// stream loops and the usage reporter: every transport funnels through observeCodexTokenEvent.
func TestObserveCodexTokenEventRecordsResponseModel(t *testing.T) {
	reporter := helps.NewExecutorUsageReporter(context.Background(), NewCodexExecutor(&config.Config{}), "gpt-6-astra", nil)

	for _, line := range bytes.Split([]byte(codexResponseModelTestStream), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		observeCodexTokenEvent(reporter, bytes.TrimSpace(line[len(dataTag):]))
	}

	if got := reporter.ResponseModel(); got != "gpt-5.6-luna" {
		t.Fatalf("reporter response model = %q, want %q", got, "gpt-5.6-luna")
	}
}

type codexResponseModelUsageCapture struct {
	alias   string
	records chan coreusage.Record
}

func (c *codexResponseModelUsageCapture) HandleUsage(_ context.Context, record coreusage.Record) {
	if record.Alias != c.alias {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type codexResponseModelNoopUsagePlugin struct{}

func (codexResponseModelNoopUsagePlugin) HandleUsage(context.Context, coreusage.Record) {}

func (c *codexResponseModelUsageCapture) await(t *testing.T) coreusage.Record {
	t.Helper()
	select {
	case record := <-c.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the usage record")
		return coreusage.Record{}
	}
}

// TestCodexUsageRecordsCarryResponseModelPerModel checks that the attempt record reports
// the served model while the image generation tool record must not claim it.
func TestCodexUsageRecordsCarryResponseModelPerModel(t *testing.T) {
	const alias = "codex-response-model-wiring-test"
	capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{})
	})

	ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
	auth := &cliproxyauth.Auth{ID: "codex-auth-1", Index: "auth-index-7", Provider: "codex"}
	reporter := helps.NewExecutorUsageReporter(ctx, NewCodexExecutor(&config.Config{}), "gpt-5.4-mini", auth)

	observeCodexTokenEvent(reporter, []byte(`{"type":"response.completed","response":{"model":"gpt-5.4-mini","usage":{"total_tokens":12}}}`))
	reporter.EnsurePublished(ctx)
	reporter.PublishAdditionalModel(ctx, "gpt-image-1.5", coreusage.Detail{TotalTokens: 5})

	attemptRecord := capture.await(t)
	if attemptRecord.Model != "gpt-5.4-mini" {
		t.Fatalf("attempt record model = %q, want %q", attemptRecord.Model, "gpt-5.4-mini")
	}
	if attemptRecord.ResponseModel != "gpt-5.4-mini" {
		t.Fatalf("attempt record response model = %q, want %q", attemptRecord.ResponseModel, "gpt-5.4-mini")
	}

	imageRecord := capture.await(t)
	if imageRecord.Model != "gpt-image-1.5" {
		t.Fatalf("image record model = %q, want %q", imageRecord.Model, "gpt-image-1.5")
	}
	if imageRecord.ResponseModel != "" {
		t.Fatalf("image record response model = %q, want empty", imageRecord.ResponseModel)
	}
}

func TestCodexCompactUsageRecordsObservedResponseModel(t *testing.T) {
	for _, test := range []struct {
		name, modelJSON, expectedModel string
	}{
		{name: "reported", modelJSON: `,"model":"gpt-5.6-luna"`, expectedModel: "gpt-5.6-luna"},
		{name: "omitted"},
		{name: "non_string", modelJSON: `,"model":123`},
	} {
		t.Run(test.name, func(t *testing.T) {
			const requestedModel = "gpt-6-astra"
			var requestedUpstreamModel string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/responses/compact" {
					http.Error(writer, "unexpected request path", http.StatusNotFound)
					return
				}
				body, errRead := io.ReadAll(request.Body)
				if errRead != nil {
					t.Errorf("read compact request: %v", errRead)
					return
				}
				requestedUpstreamModel = gjson.GetBytes(body, "model").String()
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, `{"id":"compact_1","object":"response.compaction"`+test.modelJSON+`,"output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`)
			}))
			defer server.Close()
			alias := strings.ReplaceAll(t.Name(), "/", "-")
			capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{}) })
			ctx := coreusage.WithRequestedModelAlias(t.Context(), alias)
			auth := &cliproxyauth.Auth{ID: "compact-model-auth", Provider: "codex", ProxyURL: "direct", Attributes: map[string]string{
				"api_key": "synthetic-key", "base_url": server.URL,
			}}
			_, errExecute := NewCodexExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{
				Model: requestedModel, Payload: []byte(`{"model":"gpt-6-astra","input":"hello"}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact"})
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			record := capture.await(t)
			if record.ResponseModel != test.expectedModel {
				t.Fatalf("ResponseModel=%q, want %q", record.ResponseModel, test.expectedModel)
			}
			if record.Model != requestedModel || requestedUpstreamModel != requestedModel || record.Detail.TotalTokens != 5 {
				t.Fatalf("observed response model altered request or accounting: record=%+v upstream=%q", record, requestedUpstreamModel)
			}
		})
	}
}
