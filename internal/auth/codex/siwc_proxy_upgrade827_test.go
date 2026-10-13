package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestUpgrade827SIWCRefreshKeepsProxyAndDeviceIsolation(t *testing.T) {
	for _, tc := range []struct{ name, proxyA, proxyB, deviceA, deviceB string }{
		{"different_proxy", "http://proxy-a.invalid:8080", "http://proxy-b.invalid:8080", "device-a", "device-a"},
		{"different_device", "direct", "direct", "device-a", "device-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			var once sync.Once
			var workers sync.WaitGroup
			t.Cleanup(func() { once.Do(func() { close(release) }); workers.Wait() })
			type result struct {
				data           *CodexSIWCTokenData
				err            error
				access, device string
			}
			results := make(chan result, 2)
			for i, proxy := range []string{tc.proxyA, tc.proxyB} {
				auth := NewCodexAuthWithProxyURL(nil, proxy)
				access, device := []string{"access-a", "access-b"}[i], []string{tc.deviceA, tc.deviceB}[i]
				auth.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					started <- struct{}{}
					<-release
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(`{"access_token":"` + access + `","refresh_token":"rotated","scope":"chatgpt.tokens.use.direct","expires_in":3600}`))}, nil
				})
				workers.Add(1)
				go func() {
					defer workers.Done()
					data, err := auth.RefreshSIWCTokens(t.Context(), t.Name(), "same-client", device)
					results <- result{data, err, access, device}
				}()
			}
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			for range 2 {
				select {
				case <-started:
				case <-deadline.C:
					t.Fatal("SIWC refresh incorrectly shared a result across a proxy or device boundary")
				}
			}
			once.Do(func() { close(release) })
			for range 2 {
				got := <-results
				if got.err != nil || got.data == nil || got.data.AccessToken != got.access || got.data.DeviceID != got.device {
					t.Fatalf("SIWC refresh used another route/device: error=%v", got.err)
				}
			}
		})
	}
}

func TestUpgrade827SIWCInvalidProxyFailsClosed(t *testing.T) {
	auth := NewCodexAuthWithProxyURL(nil, "invalid://synthetic-proxy")
	if _, err := auth.RefreshSIWCTokens(context.Background(), "synthetic-refresh", "synthetic-client", "synthetic-device"); err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("invalid proxy did not fail closed: %v", err)
	}
}

func TestUpgrade827SIWCSelectedProxyAndExplicitDirectUseOnlyTheirRoute(t *testing.T) {
	var direct, selected, global atomic.Int32
	respond := func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","scope":"chatgpt.tokens.use.direct","expires_in":3600}`)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { direct.Add(1); respond(w) }))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { selected.Add(1); respond(w) }))
	defer proxy.Close()
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { global.Add(1); w.WriteHeader(http.StatusBadGateway) }))
	defer canary.Close()
	previous := siwcTokenEndpointURL
	siwcTokenEndpointURL = origin.URL
	defer func() { siwcTokenEndpointURL = previous }()
	cfg := &config.Config{}
	cfg.ProxyURL = canary.URL
	for i, route := range []string{proxy.URL, "direct"} {
		data, err := NewCodexAuthWithProxyURL(cfg, route).RefreshSIWCTokens(t.Context(), t.Name(), "synthetic-client", "synthetic-device")
		if err != nil || data.AccessToken != "synthetic-access" {
			t.Fatalf("route %d: %v", i, err)
		}
		if selected.Load() != 1 || direct.Load() != int32(i) || global.Load() != 0 {
			t.Fatalf("unexpected egress: selected=%d direct=%d global=%d", selected.Load(), direct.Load(), global.Load())
		}
	}
}
