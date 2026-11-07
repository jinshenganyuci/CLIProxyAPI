package live

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/traffic"
)

type trafficReaderFromSink struct {
	bytes.Buffer
	readFromCalls int
}

func (s *trafficReaderFromSink) ReadFrom(reader io.Reader) (int64, error) {
	s.readFromCalls++
	return s.Buffer.ReadFrom(reader)
}

func TestClientTrafficWriterPreservesReadFrom(t *testing.T) {
	collector, meter := newLiveTrafficTestMeter(t)
	const payload = "websocket payload"
	sink := &trafficReaderFromSink{}
	writer := &clientTrafficWriter{Writer: sink, meter: meter}
	reader, source := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = source.Close() })
	done := make(chan error, 1)
	go func() {
		_, errCopy := io.Copy(writer, reader)
		done <- errCopy
	}()
	if _, errWrite := io.WriteString(source, payload[:4]); errWrite != nil {
		t.Fatal(errWrite)
	}
	requireLiveTraffic(t, collector, traffic.WebSocket, 0, 4)
	select {
	case errCopy := <-done:
		t.Fatalf("copy ended before the message was closed: %v", errCopy)
	default:
	}
	if _, errWrite := io.WriteString(source, payload[4:]); errWrite != nil {
		t.Fatal(errWrite)
	}
	_ = source.Close()
	if errCopy := <-done; errCopy != nil || sink.String() != payload {
		t.Fatalf("copy error = %v; payload = %q", errCopy, sink.String())
	}
	if sink.readFromCalls != 1 {
		t.Fatalf("underlying ReadFrom calls = %d, want 1", sink.readFromCalls)
	}
	requireLiveTraffic(t, collector, traffic.WebSocket, 0, uint64(len(payload)))
}

func newLiveTrafficTestMeter(t *testing.T) (*traffic.Collector, *traffic.Meter) {
	t.Helper()
	collector, errOpen := traffic.Open(traffic.Options{Enabled: true, DisableBackground: true})
	if errOpen != nil {
		t.Fatalf("open traffic collector: %v", errOpen)
	}
	t.Cleanup(func() { _ = collector.Close(context.Background()) })
	meter := collector.NewMeter()
	meter.Bind(traffic.Identity{Provider: "config-inline", Key: "synthetic-live-issuer"})
	return collector, meter
}

func requireLiveTraffic(t *testing.T, collector *traffic.Collector, protocol traffic.Protocol, rx, tx uint64) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	var actual traffic.Bytes
	for {
		rows := collector.Keys(traffic.KeyQuery{Limit: 20})
		if len(rows.Keys) == 1 {
			actual = rows.Keys[0].Traffic.Total.Protocols[protocol]
			if actual.RxBytes == rx && actual.TxBytes == tx {
				return
			}
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("client %s bytes = %+v, want rx=%d tx=%d; keys=%+v", protocol, actual, rx, tx, rows)
		}
	}
}

// DataChannels are already open when this helper is used, so one RTP packet
// establishes an exact payload count without retransmitting test application data.
func sendSingleTrafficTestRTP(t *testing.T, track *webrtc.TrackLocalStaticRTP, payload []byte, received <-chan []byte) {
	t.Helper()
	packet := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: 1, Timestamp: 960, SSRC: 1234},
		Payload: payload,
	}
	if errWrite := track.WriteRTP(packet); errWrite != nil {
		t.Fatalf("write traffic test RTP: %v", errWrite)
	}
	select {
	case actual := <-received:
		if string(actual) != string(payload) {
			t.Fatalf("RTP payload = %v, want %v", actual, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RTP payload was not relayed")
	}
}
