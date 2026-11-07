package live

import (
	"io"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/traffic"
)

// These wrappers observe only the client side of the bidirectional relay.
// They reuse the existing copy buffer and retain no message payloads.
type clientTrafficReader struct {
	io.Reader
	meter    *traffic.Meter
	transmit bool
}

func (r *clientTrafficReader) Read(payload []byte) (int, error) {
	n, errRead := r.Reader.Read(payload)
	if r.transmit {
		r.meter.AddTx(traffic.WebSocket, n)
	} else {
		r.meter.AddRx(traffic.WebSocket, n)
	}
	return n, errRead
}

type clientTrafficWriter struct {
	io.Writer
	meter          *traffic.Meter
	readFromSource clientTrafficReader
}

func (w *clientTrafficWriter) Write(payload []byte) (int, error) {
	n, errWrite := w.Writer.Write(payload)
	w.meter.AddTx(traffic.WebSocket, n)
	return n, errWrite
}

// Preserve the WebSocket writer's copy buffer instead of making io.Copy
// allocate a new buffer for every downstream message.
func (w *clientTrafficWriter) ReadFrom(reader io.Reader) (int64, error) {
	if readerFrom, ok := w.Writer.(io.ReaderFrom); ok {
		// Each read is accepted into the socket writer's own buffer. Observe
		// those chunks so a long message remains visible before it finishes.
		w.readFromSource = clientTrafficReader{Reader: reader, meter: w.meter, transmit: true}
		defer func() { w.readFromSource.Reader = nil }()
		return readerFrom.ReadFrom(&w.readFromSource)
	}
	return io.Copy(struct{ io.Writer }{w}, reader)
}
