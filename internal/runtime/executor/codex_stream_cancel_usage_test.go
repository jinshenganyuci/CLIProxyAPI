package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestCodexHTTPStreamCancellationSettlesUsageOnce(t *testing.T) {
	for _, buffering := range []bool{false, true} {
		for _, scenario := range []string{"no_usage", "partial_usage", "completed"} {
			t.Run(fmt.Sprintf("buffering_%t/%s", buffering, scenario), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					created := codexCreatedEvent
					if scenario == "partial_usage" {
						created = `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-terra","usage":{"input_tokens":11,"output_tokens":22,"total_tokens":33}}}`
					}
					_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", created, codexOutputDeltaEvent)
					if scenario == "completed" {
						_, _ = fmt.Fprintf(w, "data: %s\n\n", codexCompletedEventBody)
						return
					}
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}))
				defer server.Close()
				alias := t.Name()
				capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 8)}
				coreusage.RegisterNamedPlugin(alias, capture)
				t.Cleanup(func() { coreusage.RegisterNamedPlugin(alias, codexResponseModelNoopUsagePlugin{}) })
				ctx, cancel := context.WithCancel(coreusage.WithRequestedModelAlias(context.Background(), alias))
				defer cancel()
				req, opts := codexTestRequest()
				result, err := NewCodexExecutor(codexBufferingConfig(buffering)).ExecuteStream(ctx, codexTestAuth(server.URL), req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("unexpected stream error: %v", chunk.Err)
					}
					if scenario != "completed" && strings.Contains(string(chunk.Payload), "response.output_text.delta") {
						cancel()
					}
					if scenario == "completed" && strings.Contains(string(chunk.Payload), "response.completed") {
						cancel()
					}
				}
				coreusage.PublishRecord(context.Background(), coreusage.Record{Alias: alias, Model: "barrier"})
				var records []coreusage.Record
				for {
					record := capture.await(t)
					if record.Model == "barrier" {
						break
					}
					records = append(records, record)
				}
				if len(records) != 1 {
					t.Fatalf("settlement records = %d, want exactly one", len(records))
				}
				want := coreusage.Detail{}
				if scenario == "partial_usage" {
					want = coreusage.Detail{InputTokens: 11, OutputTokens: 22, TotalTokens: 33}
				} else if scenario == "completed" {
					want = coreusage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
				}
				got := records[0].Detail
				if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || got.TotalTokens != want.TotalTokens || records[0].Failed != (scenario != "completed") {
					t.Fatalf("settlement = %+v, want detail %+v, failed %t", records[0], want, scenario != "completed")
				}
			})
		}
	}
}
