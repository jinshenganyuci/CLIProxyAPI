package redisqueue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

type requestID804Log struct {
	id, line string
}

type requestID804Hook struct {
	lines chan requestID804Log
}

func (h *requestID804Hook) Levels() []log.Level { return log.AllLevels }

func (h *requestID804Hook) Fire(entry *log.Entry) error {
	if !strings.Contains(entry.Message, "/v1/upgrade804/") {
		return nil
	}
	line, errFormat := (&logging.LogFormatter{}).Format(entry)
	if errFormat != nil {
		return errFormat
	}
	id, _ := entry.Data["request_id"].(string)
	h.lines <- requestID804Log{id: id, line: string(line)}
	return nil
}

type requestID804Error struct{}

func (requestID804Error) Error() string   { return "synthetic upstream failure" }
func (requestID804Error) StatusCode() int { return http.StatusBadGateway }

func TestV804UUIDRequestIDCorrelatesHTTPStreamFailureUsageAndFiles(t *testing.T) {
	wasEnabled, wasUsage := redisqueue.Enabled(), redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetEnabled(wasEnabled); redisqueue.SetUsageStatisticsEnabled(wasUsage) })
	events, unsubscribe := redisqueue.SubscribeUsage()
	defer unsubscribe()
	<-events
	const total = 12
	hook := &requestID804Hook{lines: make(chan requestID804Log, total)}
	previousHooks := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	log.AddHook(hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(previousHooks) })
	logsDir := t.TempDir()
	engine := gin.New()
	engine.Use(logging.GinLogrusLogger())
	engine.Use(middleware.RequestLoggingMiddleware(logging.NewFileRequestLogger(true, logsDir, "", 0)))
	engine.POST("/v1/upgrade804/:mode/:index", func(c *gin.Context) {
		mode, index := c.Param("mode"), c.Param("index")
		model := "upgrade804-" + mode + "-" + index
		id := logging.GetGinRequestID(c)
		if id == "" || id != logging.GetRequestID(c.Request.Context()) {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		h := &handlers.BaseAPIHandler{Cfg: &config.SDKConfig{}}
		ctx, cancel := h.GetContextWithCancel(nil, c, c.Request.Context())
		defer cancel()
		reporter := helps.NewUsageReporter(ctx, "codex", model, nil)
		isStream := strings.HasPrefix(mode, "sse")
		failed := strings.HasSuffix(mode, "failure")
		reporter.SetStream(isStream)
		c.Header("X-Test-Request-ID", id)
		c.Header("X-Test-Execution-ID", reporter.RequestID())
		if isStream {
			c.Header("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", model)
			c.Writer.Flush()
			_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
		} else if failed {
			c.JSON(http.StatusBadGateway, gin.H{"error": model})
		} else {
			c.JSON(http.StatusOK, gin.H{"result": model})
		}
		detail := usage.Detail{InputTokens: 11, OutputTokens: 7, ReasoningTokens: 2, CachedTokens: 3, CacheReadTokens: 3, TotalTokens: 18}
		if failed {
			reporter.PublishFailureWithDetail(ctx, detail, requestID804Error{})
		} else {
			reporter.Publish(ctx, detail)
		}
		reporter.EnsurePublished(ctx)
	})
	server := httptest.NewServer(engine)
	defer server.Close()
	type receipt struct {
		model, id, execution, body string
		status                     int
		stream, failed             bool
		err                        error
	}
	receipts := make(chan receipt, total)
	var wg sync.WaitGroup
	for _, mode := range []string{"json", "sse", "json-failure", "sse-failure"} {
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func(mode string, i int) {
				defer wg.Done()
				r := receipt{model: fmt.Sprintf("upgrade804-%s-%d", mode, i), stream: strings.HasPrefix(mode, "sse"), failed: strings.HasSuffix(mode, "failure")}
				resp, errDo := server.Client().Post(fmt.Sprintf("%s/v1/upgrade804/%s/%d", server.URL, mode, i), "application/json", strings.NewReader(`{"input":"synthetic"}`))
				if errDo != nil {
					r.err = errDo
					receipts <- r
					return
				}
				body, errRead := io.ReadAll(resp.Body)
				errClose := resp.Body.Close()
				r.id, r.execution, r.body, r.status, r.err = resp.Header.Get("X-Test-Request-ID"), resp.Header.Get("X-Test-Execution-ID"), string(body), resp.StatusCode, errRead
				if errClose != nil {
					r.err = errClose
				}
				receipts <- r
			}(mode, i)
		}
	}
	wg.Wait()
	close(receipts)
	byModel := make(map[string]receipt, total)
	byID := make(map[string]receipt, total)
	seenExecutions := make(map[string]bool, total)
	for r := range receipts {
		if r.err != nil {
			t.Fatal(r.err)
		}
		id, errParse := uuid.Parse(r.id)
		if errParse != nil || id.Version() != 7 || id.Variant() != uuid.RFC4122 {
			t.Fatalf("invalid inbound UUIDv7: %q, %v", r.id, errParse)
		}
		execution, errExecution := uuid.Parse(r.execution)
		if errExecution != nil || execution.Version() != 4 || seenExecutions[r.execution] || r.execution == r.id {
			t.Fatalf("invalid or reused execution UUIDv4: %q", r.execution)
		}
		if _, duplicate := byID[r.id]; duplicate {
			t.Fatalf("duplicate inbound UUIDv7 %s", r.id)
		}
		if !strings.Contains(r.body, r.model) || (r.stream && !strings.Contains(r.body, "[DONE]")) {
			t.Fatalf("wrong response association for %s: %s", r.model, r.body)
		}
		wantStatus := http.StatusOK
		if r.failed && !r.stream {
			wantStatus = http.StatusBadGateway
		}
		if r.status != wantStatus {
			t.Fatalf("%s status=%d, want %d", r.model, r.status, wantStatus)
		}
		byModel[r.model], byID[r.id], seenExecutions[r.execution] = r, r, true
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for i := 0; i < total; i++ {
		select {
		case line := <-hook.lines:
			r, exists := byID[line.id]
			if !exists || !strings.Contains(line.line, "["+logging.ShortRequestID(line.id)+"]") || strings.Contains(line.line, line.id) {
				t.Fatalf("console ID mismatch: %#v", line)
			}
			paths, errGlob := filepath.Glob(filepath.Join(logsDir, "*-"+logging.ShortRequestID(line.id)+".log"))
			if errGlob != nil || len(paths) != 1 {
				t.Fatalf("log file association %s: %v (%v)", line.id, paths, errGlob)
			}
			body, errRead := os.ReadFile(paths[0])
			if errRead != nil || !strings.Contains(string(body), r.model) || strings.Contains(filepath.Base(paths[0]), line.id) {
				t.Fatalf("log file body/name association %s failed: %v", line.id, errRead)
			}
		case <-deadline.C:
			t.Fatal("HTTP log completion not observed")
		}
	}
	// All handler publications precede this FIFO barrier, including canceled request contexts.
	usage.PublishRecord(context.Background(), usage.Record{Provider: "codex", Model: "upgrade804-barrier"})
	seenModels := make(map[string]bool, total)
	for {
		select {
		case raw := <-events:
			var payload map[string]any
			if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			model, _ := payload["model"].(string)
			if model == "upgrade804-barrier" {
				if len(seenModels) != total {
					t.Fatalf("usage count=%d, want %d", len(seenModels), total)
				}
				return
			}
			r, exists := byModel[model]
			if !exists {
				continue
			}
			if seenModels[model] {
				t.Fatalf("duplicate billable record for %s", model)
			}
			seenModels[model] = true
			if payload["request_id"] != r.id || payload["trace_id"] != r.id || payload["execution_id"] != r.execution || payload["stream"] != r.stream || payload["failed"] != r.failed {
				t.Fatalf("usage IDs/outcome mismatch for %s: %#v", model, payload)
			}
			wantTokens := map[string]any{"input_tokens": float64(11), "output_tokens": float64(7), "reasoning_tokens": float64(2), "cached_tokens": float64(3), "cache_read_tokens": float64(3), "cache_read_tokens_present": true, "cache_creation_tokens": float64(0), "total_tokens": float64(18)}
			if !reflect.DeepEqual(payload["tokens"], wantTokens) {
				t.Fatalf("complete billing token fields changed: got %#v, want %#v", payload["tokens"], wantTokens)
			}
			fail, ok := payload["fail"].(map[string]any)
			wantStatus := float64(200)
			if r.failed {
				wantStatus = 502
			}
			if !ok || fail["status_code"] != wantStatus {
				t.Fatalf("upstream outcome lost for %s: %#v", model, fail)
			}
		case <-deadline.C:
			t.Fatal("usage barrier not received")
		}
	}
}

func TestV804CanceledUsageKeepsFullRequestIDAndExplicitExecutionTrace(t *testing.T) {
	wasEnabled, wasUsage := redisqueue.Enabled(), redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetEnabled(wasEnabled); redisqueue.SetUsageStatisticsEnabled(wasUsage) })
	events, unsubscribe := redisqueue.SubscribeUsage()
	defer unsubscribe()
	<-events
	const requestID = "01996ab1-1234-7abc-8ef0-1234deadbeef"
	const executionID = "12345678-1234-4234-8ef0-1234deadbeef"
	const explicitID = "87654321-1234-4234-8ef0-1234deadbeef"
	ctx := logging.WithRequestID(context.Background(), requestID)
	ctx = usage.WithExecutionRequestID(ctx, executionID)
	ctx = usage.WithTraceID(ctx, "explicit-parent-trace")
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	usage.PublishRecord(ctx, usage.Record{Provider: "codex", Model: "upgrade804-canceled-context", Detail: usage.Detail{TotalTokens: 3}})
	usage.PublishRecord(ctx, usage.Record{Provider: "codex", Model: "upgrade804-canceled-record", RequestID: explicitID, TraceID: "record-parent-trace", Detail: usage.Detail{TotalTokens: 5}})
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	seen := make(map[string]bool)
	for len(seen) < 2 {
		select {
		case raw := <-events:
			var payload map[string]any
			if errJSON := json.Unmarshal(raw, &payload); errJSON != nil {
				t.Fatal(errJSON)
			}
			model, _ := payload["model"].(string)
			if model != "upgrade804-canceled-context" && model != "upgrade804-canceled-record" {
				continue
			}
			wantExecution, wantTrace, wantTotal := executionID, "explicit-parent-trace", float64(3)
			if model == "upgrade804-canceled-record" {
				wantExecution, wantTrace, wantTotal = explicitID, "record-parent-trace", 5
			}
			if seen[model] || payload["request_id"] != requestID || payload["execution_id"] != wantExecution || payload["trace_id"] != wantTrace {
				t.Fatalf("canceled publication lost independent IDs: %#v", payload)
			}
			tokens, ok := payload["tokens"].(map[string]any)
			if !ok || tokens["total_tokens"] != wantTotal {
				t.Fatalf("canceled publication billing changed: %#v", payload)
			}
			seen[model] = true
		case <-deadline.C:
			t.Fatal("canceled request usage was not delivered")
		}
	}
}
