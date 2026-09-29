package logging

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

type uuidV804HomeAppClient struct{ pushed chan []byte }

func (c *uuidV804HomeAppClient) HeartbeatOK() bool { return true }
func (c *uuidV804HomeAppClient) RPushAppLog(_ context.Context, payload []byte) error {
	c.pushed <- append([]byte(nil), payload...)
	return nil
}

func TestV804HomeApplicationLogPreservesUUIDButFormatsShortID(t *testing.T) {
	const id = "01996ab1-1234-7abc-8ef0-1234deadbeef"
	client := &uuidV804HomeAppClient{pushed: make(chan []byte, 1)}
	forwarder := &HomeAppLogForwarder{formatter: &LogFormatter{}, queue: make(chan homeAppLogPayload, 1), stop: make(chan struct{})}
	forwarder.enabled.Store(true)
	forwarder.bind(client)
	forwarder.wg.Add(1)
	go forwarder.run()
	defer forwarder.Stop()
	entry := log.NewEntry(log.New())
	entry.Level, entry.Message = log.WarnLevel, "synthetic UUID tracing event"
	entry.Data["request_id"] = id
	if errFire := forwarder.Fire(entry); errFire != nil {
		t.Fatal(errFire)
	}
	select {
	case raw := <-client.pushed:
		var payload homeAppLogPayload
		if errJSON := json.Unmarshal(raw, &payload); errJSON != nil {
			t.Fatal(errJSON)
		}
		if payload.RequestID != id || !strings.Contains(payload.Line, "[deadbeef]") || strings.Contains(payload.Line, id) {
			t.Fatalf("Home app-log full/short ID contract mismatch: %#v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Home application log was not forwarded")
	}
}

func TestV804HomeStreamingAndFailureLogsKeepFullUUID(t *testing.T) {
	original := currentHomeRequestLogClient
	t.Cleanup(func() { currentHomeRequestLogClient = original })
	const id = "01996ab1-1234-7abc-8ef0-1234deadbeef"
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP failure", true: "SSE success"}[streaming], func(t *testing.T) {
			stub := &stubHomeRequestLogClient{heartbeatOK: true}
			currentHomeRequestLogClient = func() homeRequestLogClient { return stub }
			logger := NewFileRequestLogger(true, t.TempDir(), "", 0)
			logger.SetHomeEnabled(true)
			if streaming {
				writer, errStream := logger.LogStreamingRequest("/v1/responses", http.MethodPost, nil, []byte("synthetic stream"), id)
				if errStream != nil {
					t.Fatal(errStream)
				}
				if errStatus := writer.WriteStatus(http.StatusOK, map[string][]string{"Content-Type": {"text/event-stream"}}); errStatus != nil {
					t.Fatal(errStatus)
				}
				writer.WriteChunkAsync([]byte("data: synthetic stream response\n\n"))
				if errClose := writer.Close(); errClose != nil {
					t.Fatal(errClose)
				}
			} else if errLog := logger.LogRequest("/v1/responses", http.MethodPost, nil, []byte("synthetic failure"), http.StatusBadGateway, nil, []byte("synthetic failure response"), nil, nil, nil, nil, nil, id, time.Now(), time.Now()); errLog != nil {
				t.Fatal(errLog)
			}
			if len(stub.pushed) != 1 {
				t.Fatalf("expected one forwarded log, got %d", len(stub.pushed))
			}
			var payload homeRequestLogPayload
			if errJSON := json.Unmarshal(stub.pushed[0], &payload); errJSON != nil {
				t.Fatal(errJSON)
			}
			if payload.RequestID != id || !strings.Contains(payload.RequestLog, "response") {
				t.Fatalf("Home request log lost UUID or body: %#v", payload)
			}
		})
	}
}
