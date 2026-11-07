package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/traffic"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func newTrafficAPITestCollector(t *testing.T) *traffic.Collector {
	t.Helper()
	collector, err := traffic.Open(traffic.Options{Enabled: true, Path: filepath.Join(t.TempDir(), "traffic.db"), DisableBackground: true,
		ReadNetwork: func(string) (traffic.NetworkSample, error) { return traffic.NetworkSample{Epoch: "test"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := collector.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return collector
}

type trafficReplayAuthProvider struct{}

func (trafficReplayAuthProvider) Identifier() string { return "plugin:access-guard:test" }
func (trafficReplayAuthProvider) Authenticate(_ context.Context, request *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	request.Body = io.NopCloser(bytes.NewReader(data))
	key := request.Header.Get("X-Test-Key")
	if key == "denied" {
		return nil, sdkaccess.NewDeniedError("quota denied", http.StatusTooManyRequests)
	}
	return &sdkaccess.Result{Provider: "plugin:access-guard:test", Principal: "shared-account", Metadata: map[string]string{"key_id": key}}, nil
}

func TestTrafficHTTPCountsEncodedBodyOnceAndSeparatesVerifiedKeys(t *testing.T) {
	collector := newTrafficAPITestCollector(t)
	server := &Server{traffic: newServerTrafficState()}
	server.traffic.collector.Store(collector)
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{trafficReplayAuthProvider{}})
	router := gin.New()
	router.Use(server.trafficMiddleware(), responseCompressionMiddleware(&config.Config{}))
	router.POST("/v1/responses", AuthMiddleware(manager), func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Error(err)
		}
		c.Header("Content-Type", "text/plain")
		_, _ = c.Writer.WriteString(string(body))
	})
	ignored := func(c *gin.Context) { _, _ = io.Copy(io.Discard, c.Request.Body); c.String(http.StatusOK, "ignored") }
	router.POST("/v8/management/observability/traffic", ignored)
	router.POST("/v1/ws", ignored)
	payload := strings.Repeat("compressible business data ", 512)
	var output uint64
	for _, key := range []string{"client-a", "client-b"} {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
		request.ContentLength = -1
		request.Header.Set("X-Test-Key", key)
		request.Header.Set("Accept-Encoding", "gzip")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("response: %d %v", response.Code, response.Header())
		}
		if string(compressionTestBody(t, "gzip", response.Body.Bytes())) != payload {
			t.Fatal("traffic wrapper changed response")
		}
		output += uint64(response.Body.Len())
	}
	for _, path := range []string{"/v8/management/observability/traffic", "/v1/ws"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload)))
	}
	summary := collector.Summary()
	if summary.Business.Total.RxBytes != uint64(2*len(payload)) || summary.Business.Total.TxBytes != output {
		t.Fatalf("body replay or compressed egress counted incorrectly: %#v", summary.Business.Total)
	}
	keys := collector.Keys(traffic.KeyQuery{Limit: 50})
	if len(keys.Keys) != 2 || keys.Keys[0].ID == keys.Keys[1].ID {
		t.Fatalf("verified plugin keys merged: %#v", keys)
	}
	for _, key := range keys.Keys {
		if key.Traffic.Total.RxBytes != uint64(len(payload)) || key.Traffic.Total.TxBytes != output/2 {
			t.Fatalf("wrong key bytes: %#v", key)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("rejected-body"))
	request.Header.Set("X-Test-Key", "denied")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatal(response.Code)
	}
	summary = collector.Summary()
	if summary.Unattributed.Total.RxBytes != uint64(len("rejected-body")) || summary.Unattributed.Total.TxBytes != uint64(response.Body.Len()) {
		t.Fatal("denied request did not retain unattributed bytes")
	}
	if len(collector.Keys(traffic.KeyQuery{Limit: 50}).Keys) != 2 {
		t.Fatal("unverified input created a key identity")
	}
}

type trafficPartialHTTPWriter struct {
	header http.Header
	err    error
}

func (w *trafficPartialHTTPWriter) Header() http.Header { return w.header }
func (w *trafficPartialHTTPWriter) WriteHeader(int)     {}
func (w *trafficPartialHTTPWriter) Write(p []byte) (int, error) {
	if len(p) > 3 {
		return 3, w.err
	}
	return len(p), w.err
}
func (w *trafficPartialHTTPWriter) FlushError() error { return w.err }

func TestTrafficWriterPreservesPartialWritesAndFlushErrors(t *testing.T) {
	collector := newTrafficAPITestCollector(t)
	meter := collector.NewMeter()
	meter.Bind(traffic.Identity{Provider: "config-inline", Key: "synthetic-partial-key"})
	failure := errors.New("synthetic transport failure")
	ctx, _ := gin.CreateTestContext(&trafficPartialHTTPWriter{header: make(http.Header), err: failure})
	writer := &trafficResponseWriter{ResponseWriter: ctx.Writer, meter: meter, state: newServerTrafficState()}
	n, err := writer.Write([]byte("partial-output"))
	if n != 3 || !errors.Is(err, failure) {
		t.Fatalf("partial write changed: %d %v", n, err)
	}
	if err = httpwire.FlushResponse(writer); !errors.Is(err, failure) {
		t.Fatalf("flush error lost: %v", err)
	}
	if collector.Summary().Business.Total.TxBytes != 3 {
		t.Fatal("counted bytes not accepted by writer")
	}
}

func TestTrafficManagementReadFiltersAndRejectsInvalidQueries(t *testing.T) {
	collector := newTrafficAPITestCollector(t)
	collector.SyncNativeKeys([]traffic.Identity{{Provider: "config-inline", Key: "synthetic-unused-key", Mask: "syn…-key"}})
	server := &Server{traffic: newServerTrafficState()}
	server.traffic.collector.Store(collector)
	router := gin.New()
	router.GET("/traffic", server.getTraffic)
	router.GET("/traffic/keys", server.getTrafficKeys)
	for _, tc := range []struct {
		url    string
		status int
	}{
		{"/traffic", 200}, {"/traffic?granularity=month", 200}, {"/traffic?granularity=year", 400},
		{"/traffic?key_id=missing", 404}, {"/traffic/keys?limit=0", 400}, {"/traffic/keys?offset=-1", 400},
		{"/traffic/keys?group=upstream", 400}, {"/traffic/keys", 200},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if response.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.url, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "synthetic-unused-key") {
			t.Fatal("raw API key leaked")
		}
	}
	keys := collector.Keys(traffic.KeyQuery{Limit: 50})
	if keys.Total != 1 || keys.Keys[0].Traffic.Total.TotalBytes != 0 {
		t.Fatal("zero-traffic key missing")
	}
}
