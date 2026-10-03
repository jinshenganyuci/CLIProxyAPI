package middleware

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"testing/synctest"

	"github.com/gin-gonic/gin"
)

type drainRecordingStreamWriter struct {
	testStreamingLogWriter
	chunks [][]byte
}

func (w *drainRecordingStreamWriter) WriteChunkAsync(chunk []byte) {
	w.chunks = append(w.chunks, bytes.Clone(chunk))
}

func TestFinalizeKeepsStreamingChannelUntilDelayedWorkerDrains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		chunks := make(chan []byte, 2)
		chunks <- []byte("first")
		chunks <- []byte("second")
		done := make(chan struct{})
		writer := &drainRecordingStreamWriter{}
		wrapper := &ResponseWriterWrapper{
			ResponseWriter: ctx.Writer,
			logger:         &testRequestLogger{enabled: true},
			requestInfo:    &RequestInfo{},
			isStreaming:    true,
			streamWriter:   writer,
			chunkChannel:   chunks,
			streamDone:     done,
		}
		finished := make(chan error, 1)
		go func() { finished <- wrapper.Finalize(ctx) }()
		// Finalize is now waiting for a processor that has not been scheduled.
		synctest.Wait()
		if wrapper.chunkChannel != chunks {
			t.Error("Finalize cleared the channel before the delayed worker could read it")
			// Restore the fixture so the negative control also completes cleanly.
			wrapper.chunkChannel = chunks
		}
		if writer.closed {
			t.Error("Finalize closed the log writer before its chunks were drained")
		}
		go wrapper.processStreamingChunks(done)
		synctest.Wait()
		if errFinalize := <-finished; errFinalize != nil {
			t.Fatal(errFinalize)
		}
		if len(writer.chunks) != 2 || string(writer.chunks[0]) != "first" || string(writer.chunks[1]) != "second" || !writer.closed {
			t.Fatalf("queued log chunks were not drained before close: %q, closed=%t", writer.chunks, writer.closed)
		}
		if wrapper.chunkChannel != nil || wrapper.streamDone != nil || wrapper.streamWriter != nil {
			t.Fatal("completed stream retained its worker references")
		}
	})
}
