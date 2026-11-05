package claude

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestV856ClaudeRefusalStreamPreservesFramesAndLogsOnce(t *testing.T) {
	const transcript = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"refusal-test\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\",\"stop_details\":{\"type\":\"refusal\",\"category\":\"cyber\",\"explanation\":\"synthetic policy refusal\"}},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, test := range []struct {
		name          string
		fragmented    bool
		terminalError bool
	}{
		{name: "complete frames"},
		{name: "fragmented frames", fragmented: true},
		{name: "completed refusal with executor error", terminalError: true},
		{name: "fragmented refusal with executor error", fragmented: true, terminalError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			ctx.Header("Content-Type", "text/event-stream")
			handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: false}, nil))
			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage)
			producerDone := make(chan struct{})
			refusal := errors.New("claude refusal (cyber): synthetic policy refusal")
			go func() {
				defer close(producerDone)
				defer close(data)
				defer close(errs)
				if test.fragmented {
					for start := 0; start < len(transcript); start += 7 {
						data <- []byte(transcript[start:min(start+7, len(transcript))])
					}
				} else {
					data <- []byte(transcript)
				}
				if test.terminalError {
					errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: refusal}
				}
			}()
			var canceled error
			handler.forwardClaudeStream(ctx, ctx.Writer, func(err error) { canceled = err }, data, errs)
			<-producerDone
			if recorder.Code != http.StatusOK || recorder.Body.String() != transcript {
				t.Fatalf("committed refusal changed: status=%d, body=%q", recorder.Code, recorder.Body.String())
			}
			if test.terminalError && !errors.Is(canceled, refusal) {
				t.Fatalf("cancel cause = %v, want refusal", canceled)
			}
			if !test.terminalError && canceled != nil {
				t.Fatalf("complete stream canceled as failure: %v", canceled)
			}
			raw, _ := ctx.Get("API_RESPONSE_ERROR")
			recorded, ok := raw.([]*interfaces.ErrorMessage)
			if !ok || len(recorded) != 1 || recorded[0].StatusCode != http.StatusBadRequest ||
				!strings.Contains(recorded[0].Error.Error(), "synthetic policy refusal") {
				t.Fatalf("error-only logging must record exactly one refusal: %#v", raw)
			}
		})
	}
}
