package wstunnel

import (
	"io"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/traffic"
)

// trafficReader observes text/binary message payloads, including tunnel protocol
// envelopes, as consumed. WebSocket framing and Ping/Pong/Close are excluded,
// matching the Responses WebSocket data-message accounting boundary.
// The existing reader buffer is reused and no payload is retained or copied.
type trafficReader struct {
	io.Reader
	meter *traffic.Meter
}

func (r *trafficReader) Read(payload []byte) (int, error) {
	n, errRead := r.Reader.Read(payload)
	r.meter.AddRx(traffic.WebSocket, n)
	return n, errRead
}
