package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type imageUsageUpgrade738Capture struct {
	authID  string
	records chan coreusage.Record
}

func (c *imageUsageUpgrade738Capture) HandleUsage(_ context.Context, record coreusage.Record) {
	if record.AuthID == c.authID {
		c.records <- record
	}
}

func (c *imageUsageUpgrade738Capture) receive(t *testing.T) coreusage.Record {
	t.Helper()
	select {
	case record := <-c.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("usage dispatcher did not reach the test barrier")
		return coreusage.Record{}
	}
}

func TestOpenAIImagesUpgrade738TrailingModelReadOutcome(t *testing.T) {
	for _, body := range []struct {
		name    string
		payload string
	}{
		{"raw-json", `{"model":"served-image-model","data":[]}`},
		{"sse-without-final-newline", `data: {"model":"served-image-model","data":[]}`},
		{"sse-without-event-separator", "data: {\"model\":\"served-image-model\",\"data\":[]}\n"},
	} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/read-error-%t", body.name, failed), func(t *testing.T) {
				var upstreamRequests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					upstreamRequests.Add(1)
					if request.URL.Path != "/images/generations" {
						t.Errorf("unexpected image endpoint %q", request.URL.Path)
					}
					writer.Header().Set("Content-Type", "text/event-stream")
					length := len(body.payload)
					if failed {
						length += 20
					}
					writer.Header().Set("Content-Length", fmt.Sprint(length))
					if _, errWrite := io.WriteString(writer, body.payload); errWrite != nil {
						t.Errorf("write image stream: %v", errWrite)
					}
				}))
				defer server.Close()
				authID := t.Name()
				capture := &imageUsageUpgrade738Capture{authID: authID, records: make(chan coreusage.Record, 8)}
				coreusage.RegisterNamedPlugin(authID, capture)
				t.Cleanup(func() { coreusage.RegisterNamedPlugin(authID, multiProviderNoopUsagePlugin{}) })
				ctx := coreusage.WithRequestedModelAlias(context.Background(), "client-image-alias")
				executor := NewOpenAICompatExecutor("image-upgrade738", &config.Config{})
				auth := &coreauth.Auth{ID: authID, Provider: "image-upgrade738", ProxyURL: "direct", Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic-key"}}
				result, errExecute := executor.ExecuteStream(ctx, auth, coreexecutor.Request{
					Model: "requested-image-model", Payload: []byte(`{"model":"requested-image-model","prompt":"test image"}`),
				}, coreexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image"), Stream: true})
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				var forwarded bytes.Buffer
				var streamErrors []error
				for chunk := range result.Chunks {
					forwarded.Write(chunk.Payload)
					if chunk.Err != nil {
						streamErrors = append(streamErrors, chunk.Err)
					}
				}
				if forwarded.String() != body.payload {
					t.Errorf("stream bytes changed: %q", forwarded.String())
				}
				if failed && (len(streamErrors) != 1 || !errors.Is(streamErrors[0], io.ErrUnexpectedEOF)) {
					t.Errorf("expected one real HTTP body read error, got %v", streamErrors)
				}
				if !failed && len(streamErrors) != 0 {
					t.Errorf("successful stream returned errors: %v", streamErrors)
				}
				coreusage.PublishRecord(context.Background(), coreusage.Record{AuthID: authID, Model: "barrier"})
				record := capture.receive(t)
				if record.Model != "requested-image-model" || record.Alias != "client-image-alias" || record.ResponseModel != "served-image-model" || record.Failed != failed {
					t.Errorf("usage lost the final model or changed accounting/outcome: %+v", record)
				}
				if record.Detail.TotalTokens != 0 {
					t.Errorf("image observation invented usage: %+v", record.Detail)
				}
				if next := capture.receive(t); next.Model != "barrier" {
					t.Errorf("duplicate attempt record before barrier: %+v", next)
				}
				if got := upstreamRequests.Load(); got != 1 {
					t.Errorf("image read failure retried upstream: %d requests", got)
				}
			})
		}
	}
}

type imageUsageUpgrade738ReadSignal struct {
	io.ReadCloser
	read chan struct{}
	once sync.Once
}

func (r *imageUsageUpgrade738ReadSignal) Read(buffer []byte) (int, error) {
	n, err := r.ReadCloser.Read(buffer)
	if n > 0 {
		r.once.Do(func() { close(r.read) })
	}
	return n, err
}

func TestOpenAIImagesUpgrade738CancellationPublishesFailureOnce(t *testing.T) {
	const payload = `data: {"model":"served-image-model","data":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		if _, errWrite := io.WriteString(writer, payload); errWrite != nil {
			t.Errorf("write image stream: %v", errWrite)
		}
	}))
	defer server.Close()
	read := make(chan struct{})
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", kimiRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err == nil {
			response.Body = &imageUsageUpgrade738ReadSignal{ReadCloser: response.Body, read: read}
		}
		return response, err
	}))
	ctx = coreusage.WithRequestedModelAlias(ctx, "client-image-alias")
	authID := t.Name()
	capture := &imageUsageUpgrade738Capture{authID: authID, records: make(chan coreusage.Record, 8)}
	coreusage.RegisterNamedPlugin(authID, capture)
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(authID, multiProviderNoopUsagePlugin{}) })
	auth := &coreauth.Auth{ID: authID, Provider: "image-upgrade738", Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic-key"}}
	result, errExecute := NewOpenAICompatExecutor("image-upgrade738", &config.Config{}).ExecuteStream(ctx, auth, coreexecutor.Request{
		Model: "requested-image-model", Payload: []byte(`{"model":"requested-image-model","prompt":"test image"}`),
	}, coreexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image"), Stream: true})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("real upstream image response was not read")
	}
	// No consumer can receive the unbuffered stream, so cancellation owns exit.
	cancel()
	record := capture.receive(t)
	if !record.Failed || record.Model != "requested-image-model" || record.Alias != "client-image-alias" || record.ResponseModel != "served-image-model" {
		t.Errorf("cancelled image stream became success or lost identity/model: %+v", record)
	}
	for chunk := range result.Chunks {
		t.Errorf("stream forwarded output after cancellation: %+v", chunk)
	}
	coreusage.PublishRecord(context.Background(), coreusage.Record{AuthID: authID, Model: "barrier"})
	if next := capture.receive(t); next.Model != "barrier" {
		t.Errorf("cancelled attempt was published twice: %+v", next)
	}
}
