package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

func TestPendingStreamErrorReturnsBufferedError(t *testing.T) {
	errs := make(chan *interfaces.ErrorMessage, 1)
	want := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("upstream failed")}
	errs <- want
	close(errs)

	got, ok := PendingStreamError(errs)
	if !ok || got != want {
		t.Fatalf("PendingStreamError() = (%#v, %t), want (%#v, true)", got, ok, want)
	}
}

func TestValidateSSEDataJSONAllowsMultilinePayload(t *testing.T) {
	chunk := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"status\":\"completed\"}}\n\n")
	if err := validateSSEDataJSON(chunk); err != nil {
		t.Fatalf("validateSSEDataJSON() error = %v, want nil", err)
	}
}

func TestForwardStreamNormalizesErrorBeforeWriteAndCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("raw secret")}
	close(errs)

	var written, canceled string
	disabledKeepAlive := time.Duration(0)
	h := &BaseAPIHandler{}
	h.ForwardStream(c, recorder, func(err error) {
		if err != nil {
			canceled = err.Error()
		}
	}, data, errs, StreamForwardOptions{
		KeepAliveInterval: &disabledKeepAlive,
		NormalizeTerminalError: func(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
			return &interfaces.ErrorMessage{StatusCode: errMsg.StatusCode, Error: errors.New("safe error")}
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			written = errMsg.Error.Error()
		},
	})

	if written != "safe error" || canceled != "safe error" {
		t.Fatalf("written=%q canceled=%q, want sanitized error", written, canceled)
	}
}

func TestPendingStreamErrorIgnoresUnavailableErrors(t *testing.T) {
	closed := make(chan *interfaces.ErrorMessage)
	close(closed)

	for name, errs := range map[string]<-chan *interfaces.ErrorMessage{
		"nil":          nil,
		"closed empty": closed,
		"open empty":   make(chan *interfaces.ErrorMessage),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := PendingStreamError(errs); ok || got != nil {
				t.Fatalf("PendingStreamError() = (%#v, %t), want (nil, false)", got, ok)
			}
		})
	}
}

func TestSplitCRLFRepro(t *testing.T) {
	state := &sseJSONValidationState{}
	chunks := []string{
		"data: {\"type\":\"response.completed\",\r",
		"\ndata: \"response\":{\"status\":\"completed\"}}\r\n\r\n",
	}
	var output []byte
	for _, chunk := range chunks {
		out, errAdd := state.AddChunk([]byte(chunk))
		if errAdd != nil {
			t.Fatal(errAdd)
		}
		output = append(output, out...)
	}
	if errFinish := state.Finish(); errFinish != nil {
		t.Fatal(errFinish)
	}

	singleState := &sseJSONValidationState{}
	unsplitChunk := "data: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"status\":\"completed\"}}\r\n\r\n"
	singleOutput, errSingle := singleState.AddChunk([]byte(unsplitChunk))
	if errSingle != nil {
		t.Fatal(errSingle)
	}
	if errFinish := singleState.Finish(); errFinish != nil {
		t.Fatal(errFinish)
	}
	if string(output) != string(singleOutput) {
		t.Fatalf("output mismatch: got %q, want %q", string(output), string(singleOutput))
	}
}

func TestSSEJSONValidationStateSplitCRLFVariants(t *testing.T) {
	wantOutput := "data: {\"type\":\"response.completed\",\ndata: \"response\":{\"status\":\"completed\"}}\n\n"
	tests := []struct {
		name   string
		chunks []string
	}{
		{
			name: "intervening empty chunks",
			chunks: []string{
				"data: {\"type\":\"response.completed\",\r",
				"",
				"",
				"\ndata: \"response\":{\"status\":\"completed\"}}\r\n\r\n",
			},
		},
		{
			name: "standalone LF chunk",
			chunks: []string{
				"data: {\"type\":\"response.completed\",\r",
				"\n",
				"data: \"response\":{\"status\":\"completed\"}}\r\n\r\n",
			},
		},
		{
			name: "bare CR chunk followed by regular content",
			chunks: []string{
				"data: {\"type\":\"response.completed\",\r",
				"data: \"response\":{\"status\":\"completed\"}}\r\n\r\n",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := &sseJSONValidationState{}
			var output []byte
			for _, chunk := range tc.chunks {
				out, errAdd := state.AddChunk([]byte(chunk))
				if errAdd != nil {
					t.Fatalf("AddChunk failed: %v", errAdd)
				}
				output = append(output, out...)
			}
			if errFinish := state.Finish(); errFinish != nil {
				t.Fatalf("Finish failed: %v", errFinish)
			}
			if string(output) != wantOutput {
				t.Fatalf("output mismatch: got %q, want %q", string(output), wantOutput)
			}
		})
	}
}

func TestV844ForwardStreamCanceledClosePreservesCancellationAndRealEOF(t *testing.T) {
	for _, canceled := range []bool{true, false} {
		name := "real-eof"
		if canceled {
			name = "canceled-close"
		}
		t.Run(name, func(t *testing.T) {
			const attempts = 128
			wrong, closeChecks := 0, 0
			for range attempts {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				ctx, cancel := context.WithCancel(t.Context())
				c.Request = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
				data := make(chan []byte)
				close(data)
				if canceled {
					cancel()
				}
				wantEOF := errors.New("upstream closed without terminal")
				var outcome error
				keepAlive := time.Duration(0)
				(&BaseAPIHandler{}).ForwardStream(c, recorder, func(err error) { outcome = err }, data, nil, StreamForwardOptions{
					KeepAliveInterval: &keepAlive,
					CloseError: func() *interfaces.ErrorMessage {
						closeChecks++
						return &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: wantEOF}
					},
				})
				cancel()
				if canceled && !errors.Is(outcome, context.Canceled) || !canceled && !errors.Is(outcome, wantEOF) {
					wrong++
				}
			}
			if wrong != 0 || canceled && closeChecks != 0 || !canceled && closeChecks != attempts {
				t.Fatalf("closed-channel outcome: wrong=%d close-checks=%d canceled=%v", wrong, closeChecks, canceled)
			}
		})
	}
}
