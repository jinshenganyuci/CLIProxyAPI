package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
)

type v815ErrorFlushRecorder struct {
	*httptest.ResponseRecorder
	err        error
	errorCalls int
	voidCalls  int
}

func (w *v815ErrorFlushRecorder) FlushError() error {
	w.errorCalls++
	return w.err
}

func (w *v815ErrorFlushRecorder) Flush() {
	w.voidCalls++
}

type v815LegacyFlushWrapper struct {
	gin.ResponseWriter
	flushes int
}

func (w *v815LegacyFlushWrapper) Flush() {
	w.flushes++
	w.ResponseWriter.Flush()
}

func TestV815LoggingWriterPreservesFlushErrorAndDelayedDrain(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "flush-error"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sentinel := errors.New("synthetic downstream flush failure")
				recorder := &v815ErrorFlushRecorder{ResponseRecorder: httptest.NewRecorder()}
				if fail {
					recorder.err = sentinel
				}
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				writer := &drainRecordingStreamWriter{}
				wrapper := NewResponseWriterWrapper(ctx.Writer, &testRequestLogger{enabled: true}, &RequestInfo{})
				chunks := make(chan []byte, 2)
				done := make(chan struct{})
				wrapper.isStreaming, wrapper.streamWriter, wrapper.chunkChannel, wrapper.streamDone = true, writer, chunks, done
				wrapper.Header().Set("Content-Type", "text/event-stream")
				if _, errWrite := wrapper.Write([]byte("data: first\n\n")); errWrite != nil {
					t.Fatal(errWrite)
				}
				errFlush := httpwire.FlushResponse(wrapper)
				if errors.Is(errFlush, sentinel) != fail || (errFlush != nil) != fail || recorder.errorCalls != 1 || recorder.voidCalls != 0 {
					t.Fatalf("flush contract changed: error=%v errorCalls=%d voidCalls=%d", errFlush, recorder.errorCalls, recorder.voidCalls)
				}
				if !ctx.Writer.Written() || recorder.Body.String() != "data: first\n\n" {
					t.Fatal("flush bypassed Gin header accounting or response writes")
				}
				finished := make(chan error, 1)
				go func() { finished <- wrapper.Finalize(ctx) }()
				synctest.Wait()
				if wrapper.chunkChannel != chunks || writer.closed {
					t.Error("flush completion discarded the delayed log worker")
					wrapper.chunkChannel = chunks
				}
				go wrapper.processStreamingChunks(done)
				synctest.Wait()
				if errFinalize := <-finished; errFinalize != nil {
					t.Fatal(errFinalize)
				}
				if len(writer.chunks) != 1 || string(writer.chunks[0]) != "data: first\n\n" || !writer.closed || wrapper.chunkChannel != nil || wrapper.streamDone != nil {
					t.Fatal("flush result changed SSE log drain or worker cleanup")
				}
			})
		})
	}
}

func TestV815LoggingWriterDoesNotBypassLegacyMiddlewareFlush(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	legacy := &v815LegacyFlushWrapper{ResponseWriter: ctx.Writer}
	wrapper := NewResponseWriterWrapper(legacy, &testRequestLogger{}, &RequestInfo{})
	if errFlush := httpwire.FlushResponse(wrapper); errFlush != nil {
		t.Fatal(errFlush)
	}
	if legacy.flushes != 1 || !ctx.Writer.Written() {
		t.Fatalf("legacy buffering/compression flush bypassed: calls=%d written=%t", legacy.flushes, ctx.Writer.Written())
	}
}
