package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestV857LegacyImageFailureRestoresIdentityAndRetainsUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, usageAt := range []string{"terminal", "earlier", "absent"} {
			t.Run(fmt.Sprintf("stream=%t/usage=%s", stream, usageAt), func(t *testing.T) {
				const originalSession = "image-client-session857"
				const usageJSON = `"usage":{"input_tokens":17,"output_tokens":5,"total_tokens":22,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}`
				mappedSessions := make(chan string, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/responses" {
						t.Errorf("legacy image route = %q, want /responses", r.URL.Path)
					}
					if _, errRead := io.Copy(io.Discard, r.Body); errRead != nil {
						t.Error(errRead)
						return
					}
					mappedSession := codexSessionHeaderValue(r.Header)
					mappedSessions <- mappedSession
					w.Header().Set("Content-Type", "text/event-stream")
					if usageAt == "earlier" {
						_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.in_progress\",\"response\":{%s}}\n\n", usageJSON)
					}
					terminalUsage := ""
					if usageAt == "terminal" {
						terminalUsage = "," + usageJSON
					}
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"model\":\"gpt-5.4-mini\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"invalid_value\",\"param\":\"complexity\",\"message\":\"Complexity 'medium' is not supported for this image model.\",\"session_id\":%q,\"note\":%q}%s}}\n\n", mappedSession, mappedSession, terminalUsage)
				}))
				defer server.Close()

				auth := codexCredentialIdentityTestAuth(t.Name(), "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
				auth.Attributes = map[string]string{"base_url": server.URL}
				auth.ProxyURL = "direct"
				capture := &providerV810UsageCapture{authID: auth.ID, records: make(chan usage.Record, 8)}
				usage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })

				exec := NewCodexExecutor(codexCredentialIdentityTestConfig(false))
				// A non-built-in image name selects the legacy Images-to-Responses bridge.
				req := cliproxyexecutor.Request{Model: "legacy-image857", Payload: []byte(`{"model":"legacy-image857","prompt":"synthetic image","prompt_cache_key":"image-client-session857"}`)}
				opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, stream)
				opts.Headers = http.Header{"Session-Id": {originalSession}}
				var terminalErr error
				errorCount := 0
				if stream {
					result, errStart := exec.ExecuteStream(context.Background(), auth, req, opts)
					if errStart != nil {
						t.Fatalf("start legacy image stream: %v", errStart)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							terminalErr = chunk.Err
							errorCount++
						}
						if len(chunk.Payload) != 0 {
							t.Errorf("terminal failure produced image payload: %s", chunk.Payload)
						}
					}
				} else {
					_, terminalErr = exec.Execute(context.Background(), auth, req, opts)
					if terminalErr != nil {
						errorCount++
					}
				}
				if terminalErr == nil || errorCount != 1 {
					t.Fatalf("terminal errors = %d, error = %v", errorCount, terminalErr)
				}
				if status, ok := terminalErr.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadRequest {
					t.Errorf("image error did not retain 400: %v", terminalErr)
				}
				mapped := <-mappedSessions
				if mapped == "" || mapped == originalSession {
					t.Fatalf("upstream session was not mapped: %q", mapped)
				}
				if got := ojson.Get(terminalErr.Error(), "error.session_id").String(); got != originalSession {
					t.Errorf("downstream error session = %q, want %q", got, originalSession)
				}
				if got := ojson.Get(terminalErr.Error(), "error.note").String(); got != mapped {
					t.Errorf("unrelated error field was rewritten: %q", got)
				}
				if strings.Count(terminalErr.Error(), "/v1/images/generations") != 1 {
					t.Errorf("remediation guidance missing or duplicated: %v", terminalErr)
				}
				records := providerV810UsageRecords(t, capture)
				if len(records) != 1 {
					t.Fatalf("failure usage records = %d, want exactly one", len(records))
				}
				record := records[0]
				if !record.Failed || record.Fail.StatusCode != http.StatusBadRequest {
					t.Errorf("failure accounting changed: %+v", record)
				}
				wantInput, wantOutput, wantTotal, wantCached, wantReasoning := int64(17), int64(5), int64(22), int64(3), int64(2)
				if usageAt == "absent" {
					wantInput, wantOutput, wantTotal, wantCached, wantReasoning = 0, 0, 0, 0, 0
				}
				got := record.Detail
				if got.InputTokens != wantInput || got.OutputTokens != wantOutput || got.TotalTokens != wantTotal || got.CachedTokens != wantCached || got.ReasoningTokens != wantReasoning {
					t.Errorf("observed failure usage lost: %+v", got)
				}
			})
		}
	}
}
