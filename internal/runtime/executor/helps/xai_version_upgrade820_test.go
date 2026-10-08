package helps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type delayedXAIVersionBody820 struct {
	entered chan struct{}
	release chan struct{}
	reader  *strings.Reader
	started bool
}

func (b *delayedXAIVersionBody820) Read(out []byte) (int, error) {
	if !b.started {
		b.started = true
		close(b.entered)
		<-b.release
	}
	return b.reader.Read(out)
}

func (*delayedXAIVersionBody820) Close() error { return nil }

func TestXAIVersionCanceledFetchCannotOverwriteNewServiceVersionUpgrade820(t *testing.T) {
	restoreVersion := SetXAIClientVersionForTest("1.0.60")
	defer restoreVersion()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &delayedXAIVersionBody820{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"version":"1.0.61"}`)}
	oldClient := &http.Client{Transport: utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: req}, nil
	})}
	done := make(chan struct{})
	go func() {
		refreshXAIClientVersionWithClient(ctx, oldClient)
		close(done)
	}()
	<-body.entered
	// A custom transport may finish reading even after its service was canceled.
	cancel()
	newClient := &http.Client{Transport: utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"version":"1.0.70"}`)), Request: req}, nil
	})}
	refreshXAIClientVersionWithClient(context.Background(), newClient)
	close(body.release)
	<-done
	if got := GetXAIClientVersion(); got != "1.0.70" {
		t.Fatalf("canceled old fetch overwrote the active service version: %q", got)
	}
}

func TestXAIVersionSelectedProxyNeverFallsBackUpgrade820(t *testing.T) {
	var originCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		_, _ = w.Write([]byte(`{"version":"1.0.90"}`))
	}))
	defer origin.Close()
	restoreURL := OverrideXAINPMRegistryURLForTest(origin.URL)
	defer restoreURL()
	restoreVersion := SetXAIClientVersionForTest("1.0.60")
	defer restoreVersion()
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	for _, selected := range []string{proxy.URL, "invalid://selected-proxy"} {
		t.Run(selected, func(t *testing.T) {
			restoreProxy := setXAIVersionProxyURLForTest(selected)
			defer restoreProxy()
			refreshXAIClientVersion(context.Background())
			if originCalls.Load() != 0 || GetXAIClientVersion() != "1.0.60" {
				t.Fatal("selected proxy failure reached origin or changed cached version")
			}
		})
	}
	if proxyCalls.Load() != 1 {
		t.Fatalf("proxy calls=%d, want exactly one without retry", proxyCalls.Load())
	}
}

func TestXAIVersionFetchUsesServiceCancellationWithoutReadDeadlineUpgrade820(t *testing.T) {
	for _, selected := range []string{"", "direct", "http://127.0.0.1:1"} {
		restoreProxy := setXAIVersionProxyURLForTest(selected)
		client := xaiVersionHTTPClient()
		restoreProxy()
		if client.Timeout != 0 {
			t.Fatalf("version lookup configured a body read timeout for route %q", selected)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	var deadlinePresent atomic.Bool
	client := &http.Client{Transport: utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		_, hasDeadline := req.Context().Deadline()
		deadlinePresent.Store(hasDeadline)
		close(entered)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	done := make(chan error, 1)
	go func() {
		_, errFetch := FetchXAINPMLatestVersion(ctx, client)
		done <- errFetch
	}()
	<-entered
	cancel()
	if errFetch := <-done; !errors.Is(errFetch, context.Canceled) {
		t.Fatalf("fetch did not retain service cancellation: %v", errFetch)
	}
	if deadlinePresent.Load() {
		t.Fatal("lookup introduced a post-connection request deadline")
	}
}
