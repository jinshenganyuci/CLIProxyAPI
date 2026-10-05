package executor

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestGeminiTerminalAndFailedUsageComposeUpgrade812(t *testing.T) {
	for _, provider := range issue6258Providers {
		t.Run(provider, func(t *testing.T) {
			for _, ending := range []string{"complete", "max_tokens", "truncated"} {
				t.Run(ending, func(t *testing.T) {
					capture := &providerV810UsageCapture{authID: t.Name(), records: make(chan usage.Record, 8)}
					usage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })
					finish := "STOP"
					if ending == "max_tokens" {
						finish = "MAX_TOKENS"
					}
					body := issue6258SSE(provider, issue6258ExecutorContent, false) + issue6258SSE(provider, fmt.Sprintf(`{"candidates":[{"finishReason":%q}]}`, finish), false) + issue6258SSE(provider, issue6258ExecutorUsage, false)
					if ending == "truncated" {
						body = issue6258SSE(provider, issue6258ExecutorContent, false) + issue6258SSE(provider, issue6258ExecutorUsage, false)
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if issue6258TokenResponse(w, r) {
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						if ending == "truncated" {
							w.Header().Set("Content-Length", fmt.Sprint(len(body)+100))
						}
						_, _ = io.WriteString(w, body)
					}))
					defer server.Close()
					ctx, executor, auth := issue6258Executor(t, provider, server.URL)
					terminals, errors := 0, 0
					for chunk := range issue6258StartStream(t, ctx, executor, auth, `{"input":"hello","stream":true}`) {
						if chunk.Err != nil {
							errors++
						}
						for _, event := range issue6258Events(chunk.Payload) {
							kind := event.Get("type").String()
							if kind != "response.completed" && kind != "response.incomplete" && kind != "response.failed" {
								continue
							}
							terminals++
							want := "response.completed"
							if ending == "max_tokens" {
								want = "response.incomplete"
							}
							if kind != want || ending == "truncated" {
								t.Fatalf("unexpected terminal %s for %s", kind, ending)
							}
							if event.Get("response.usage.total_tokens").Int() != 160 || event.Get("response.usage.output_tokens").Int() != 60 {
								t.Fatal("terminal omitted split trailing usage")
							}
							if ending == "max_tokens" && event.Get("response.incomplete_details.reason").String() != "max_output_tokens" {
								t.Fatal("MAX_TOKENS reason lost")
							}
						}
					}
					if ending == "truncated" {
						if errors != 1 || terminals != 0 {
							t.Fatalf("truncated stream errors=%d terminals=%d", errors, terminals)
						}
					} else if errors != 0 || terminals != 1 {
						t.Fatalf("clean terminal errors=%d terminals=%d", errors, terminals)
					}
					var records []usage.Record
					if provider == "antigravity" {
						records = antigravityUsageRecordsAfterDelivery815(t, capture)
					} else {
						records = providerV810UsageRecords(t, capture)
					}
					if len(records) != 1 {
						t.Fatalf("usage records=%d, want exactly one", len(records))
					}
					record := records[0]
					if record.Failed != (ending == "truncated") || record.Detail.InputTokens != 100 || record.Detail.OutputTokens != 10 || record.Detail.ReasoningTokens != 50 || record.Detail.TotalTokens != 160 {
						t.Fatalf("terminal and usage mismatch: failed=%t detail=%+v", record.Failed, record.Detail)
					}
				})
			}
		})
	}
}
