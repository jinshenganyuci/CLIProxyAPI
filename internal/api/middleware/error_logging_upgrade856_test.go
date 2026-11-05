package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestV856TerminalFailureLogSurvivesLaterCancellation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "error_only", true: "all_requests"}[enabled], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			dir := t.TempDir()
			router := gin.New()
			router.Use(RequestLoggingMiddleware(logging.NewFileRequestLogger(enabled, dir, "", 10)))
			router.POST("/v1/responses", func(c *gin.Context) {
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				c.Header("Content-Type", "text/event-stream")
				c.Header("X-Request-Marker", "retained-header")
				c.Status(http.StatusOK)
				if _, errWrite := c.Writer.Write([]byte("data: {\"type\":\"response.created\"}\n\n")); errWrite != nil {
					t.Fatal(errWrite)
				}
				c.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{
					nil,
					{StatusCode: http.StatusBadRequest, Error: errors.New("claude refusal (cyber): synthetic terminal failure")},
					{StatusCode: clienterror.StatusClientClosedRequest, Error: context.Canceled},
				})
				if _, errWrite := c.Writer.WriteString("event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"synthetic terminal failure\"}}\n\n"); errWrite != nil {
					t.Fatal(errWrite)
				}
				cancel()
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"synthetic"}`)))
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "response.created") || !strings.Contains(recorder.Body.String(), "synthetic terminal failure") {
				t.Fatalf("logging changed the committed response: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			entries, errRead := os.ReadDir(dir)
			if errRead != nil {
				t.Fatal(errRead)
			}
			var logs []string
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
					logs = append(logs, filepath.Join(dir, entry.Name()))
				}
			}
			if len(logs) != 1 {
				t.Fatalf("terminal failure should produce one error log, got %v", logs)
			}
			body, errRead := os.ReadFile(logs[0])
			if errRead != nil {
				t.Fatal(errRead)
			}
			for _, want := range []string{"=== RESPONSE ===\nStatus: 400\n", "HTTP Status: 400", "synthetic terminal failure", "X-Request-Marker: retained-header"} {
				if !strings.Contains(string(body), want) {
					t.Fatalf("final cancellation hid failure or headers: missing %q in %s", want, body)
				}
			}
		})
	}
}
