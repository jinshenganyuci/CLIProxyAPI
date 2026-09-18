package xai

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestXAIProxyUpgrade737InvalidProxyCannotReachDefaultOrGlobalRoute(t *testing.T) {
	var directCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "unexpected-direct-token", "device_code": "unexpected-device", "user_code": "CODE", "verification_uri": "https://accounts.x.ai"})
	}))
	defer origin.Close()
	var globalCalls atomic.Int32
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "unexpected-global-token"})
	}))
	defer global.Close()
	for _, proxyURL := range []string{"unsupported://fixture-user:fixture-password@proxy.invalid", "://fixture-password", "http://"} {
		for _, globalURL := range []string{"", global.URL} {
			auth := NewXAIAuthWithProxyURL(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: globalURL}}, proxyURL)
			checks := []func() error{
				func() error {
					_, err := auth.RequestDeviceCode(context.Background(), origin.URL, origin.URL)
					return err
				},
				func() error {
					_, err := auth.RefreshTokens(context.Background(), "synthetic-refresh", origin.URL)
					return err
				},
				func() error {
					_, err, _, _ := auth.exchangeDeviceCode(context.Background(), origin.URL, "synthetic-device", time.Second)
					return err
				},
			}
			for _, check := range checks {
				if err := check(); err == nil || strings.Contains(err.Error(), "fixture-password") || strings.Contains(err.Error(), "fixture-user") || strings.Contains(err.Error(), "proxy.invalid") {
					t.Errorf("invalid proxy succeeded or exposed proxy material: %v", err)
				}
			}
		}
	}
	if directCalls.Load() != 0 || globalCalls.Load() != 0 {
		t.Fatalf("invalid proxy fallback: direct=%d global=%d", directCalls.Load(), globalCalls.Load())
	}
}

func xaiUpgrade737ForwardProxy(t *testing.T, sourceIP string) *httptest.Server {
	t.Helper()
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(sourceIP)}}).DialContext}
	t.Cleanup(transport.CloseIdleConnections)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := r.Clone(r.Context())
		request.RequestURI = ""
		response, err := transport.RoundTrip(request)
		if err != nil {
			http.Error(w, "controlled proxy failed", http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestXAIProxyUpgrade737RefreshDoesNotShareAcrossRoutesOrEndpoints(t *testing.T) {
	for _, boundary := range []string{"proxy-route", "token-endpoint"} {
		t.Run(boundary, func(t *testing.T) {
			firstStarted := make(chan struct{})
			secondStarted := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
			var calls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				peer, _, _ := net.SplitHostPort(r.RemoteAddr)
				isFirst := (boundary == "proxy-route" && peer == "127.0.0.2") || (boundary == "token-endpoint" && r.URL.Path == "/first")
				if isFirst {
					close(firstStarted)
					<-release
				} else {
					close(secondStarted)
				}
				if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "same-synthetic-refresh-"+boundary {
					t.Error("refresh token form changed")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": peer + r.URL.Path, "refresh_token": "rotated-refresh", "expires_in": 3600})
			}))
			defer origin.Close()
			defer releaseFirst()
			proxyA := xaiUpgrade737ForwardProxy(t, "127.0.0.2")
			proxyB := proxyA
			endpointA, endpointB := origin.URL+"/token", origin.URL+"/token"
			wantA, wantB := "127.0.0.2/token", "127.0.0.3/token"
			if boundary == "proxy-route" {
				proxyB = xaiUpgrade737ForwardProxy(t, "127.0.0.3")
			} else {
				endpointA, endpointB = origin.URL+"/first", origin.URL+"/second"
				wantA, wantB = "127.0.0.2/first", "127.0.0.2/second"
			}
			type outcome struct {
				token *TokenData
				err   error
			}
			resultsA, resultsB := make(chan outcome, 1), make(chan outcome, 1)
			refresh := func(proxyURL, endpoint string, result chan<- outcome) {
				token, err := NewXAIAuthWithProxyURL(nil, proxyURL).RefreshTokens(context.Background(), "same-synthetic-refresh-"+boundary, endpoint)
				result <- outcome{token, err}
			}
			go refresh(proxyA.URL, endpointA, resultsA)
			select {
			case <-firstStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("first upstream did not start")
			}
			go refresh(proxyB.URL, endpointB, resultsB)
			independent := false
			select {
			case <-secondStarted:
				independent = true
			case <-time.After(time.Second):
			}
			releaseFirst()
			for _, item := range []struct {
				result <-chan outcome
				want   string
			}{{resultsA, wantA}, {resultsB, wantB}} {
				select {
				case result := <-item.result:
					if result.err != nil || result.token == nil || result.token.AccessToken != item.want {
						t.Errorf("independent refresh result=%+v err=%v want=%q", result.token, result.err, item.want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("refresh did not complete")
				}
			}
			if !independent || calls.Load() != 2 {
				t.Fatalf("refresh crossed %s boundary: independent=%v upstream_calls=%d", boundary, independent, calls.Load())
			}
		})
	}
}
