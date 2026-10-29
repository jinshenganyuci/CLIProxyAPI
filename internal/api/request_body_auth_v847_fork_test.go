package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestV847LimitedIncompleteBodyRejectsUnauthenticatedBeforeReading(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v8/management/credentials/fields"} {
		t.Run(endpoint, func(t *testing.T) {
			cfg := parseTestServerConfigYAML(t, "config-version: 8\nserver: {max-request-body-bytes: 2048}\naccess: {api-keys: [synthetic-key]}\nmanagement: {allow-remote: true, secret-key: synthetic-management}\n")
			server := newTestServerWithConfig(t, cfg)
			counter := &countingBodyReader{reader: strings.NewReader(strings.Repeat("x", 4096))}
			req := httptest.NewRequest(http.MethodPatch, endpoint, counter)
			if endpoint == "/v1/chat/completions" {
				req.Method = http.MethodPost
			}
			req.ContentLength = -1
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || counter.read.Load() != 0 {
				t.Fatalf("unauthenticated status=%d read=%d, want 401/0", rec.Code, counter.read.Load())
			}
			httpServer := httptest.NewServer(wrapUnconsumedRequestBodyHandler(server.engine))
			defer httpServer.Close()
			conn, errDial := net.Dial("tcp", strings.TrimPrefix(httpServer.URL, "http://"))
			if errDial != nil {
				t.Fatal(errDial)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Error(errClose)
				}
			}()
			// The sender intentionally withholds the end of the first chunk. Returning
			// 401 must not depend on a body read, even when a finite cap is enabled.
			if _, errWrite := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: synthetic\r\nTransfer-Encoding: chunked\r\nContent-Type: application/json\r\n\r\n1\r\n{", req.Method, endpoint); errWrite != nil {
				t.Fatal(errWrite)
			}
			if errDeadline := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); errDeadline != nil {
				t.Fatal(errDeadline)
			}
			response, errRead := http.ReadResponse(bufio.NewReader(conn), req)
			if errRead != nil {
				t.Fatalf("incomplete unauthenticated body blocked response: %v", errRead)
			}
			if errClose := response.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if response.StatusCode != http.StatusUnauthorized || !response.Close {
				t.Fatalf("socket status/close=%d/%v", response.StatusCode, response.Close)
			}
		})
	}
}
