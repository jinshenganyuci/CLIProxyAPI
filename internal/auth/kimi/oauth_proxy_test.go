package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestKimiOAuthSelectedProxyCoversDeviceExchangeAndRefresh(t *testing.T) {
	for _, domain := range []string{KimiDefaultDomain, KimiAIDomain} {
		t.Run(domain, func(t *testing.T) {
			var selectedHits, globalHits atomic.Int32
			global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { globalHits.Add(1); w.WriteHeader(502) }))
			defer global.Close()
			selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				selectedHits.Add(1)
				if r.URL.Host != "auth."+domain || r.Header.Get("X-Msh-Device-Id") != "selected-device" {
					t.Errorf("unexpected endpoint or device: %s", r.URL)
				}
				if errParse := r.ParseForm(); errParse != nil {
					t.Error(errParse)
				}
				if r.Form.Get("client_id") != kimiClientID {
					t.Error("missing OAuth client ID")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/oauth/device_authorization" {
					_, _ = io.WriteString(w, `{"device_code":"fixture-device","user_code":"fixture-user","verification_uri":"https://kimi.invalid/authorize","expires_in":60}`)
				} else if r.URL.Path == "/api/oauth/token" {
					_, _ = io.WriteString(w, `{"access_token":"selected-token","refresh_token":"selected-refresh","token_type":"Bearer","expires_in":3600}`)
				} else {
					t.Errorf("unexpected OAuth path %q", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer selected.Close()
			client := NewDeviceFlowClientWithDomainDeviceIDAndProxyURL(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL}}, domain, "selected-device", selected.URL)
			client.oauthHost = "http://auth." + domain
			flow, errStart := client.RequestDeviceCode(context.Background())
			if errStart != nil {
				t.Fatal(errStart)
			}
			token, errExchange, pending := client.exchangeDeviceCode(context.Background(), flow.DeviceCode)
			if errExchange != nil || pending || token.AccessToken != "selected-token" {
				t.Fatalf("exchange failed: %v", errExchange)
			}
			token, errRefresh := client.RefreshToken(context.Background(), "selected-refresh")
			if errRefresh != nil || token.AccessToken != "selected-token" {
				t.Fatalf("refresh failed: %v", errRefresh)
			}
			if selectedHits.Load() != 3 || globalHits.Load() != 0 {
				t.Fatalf("route counts selected=%d global=%d", selectedHits.Load(), globalHits.Load())
			}
		})
	}
}

func TestKimiOAuthInvalidProxyDoesNotReachOriginOrGlobal(t *testing.T) {
	var hits atomic.Int32
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer canary.Close()
	for _, selected := range []string{"invalid://user:secret@proxy.invalid", "http://", "socks5://", "http://127.0.0.1:1"} {
		t.Run(selected, func(t *testing.T) {
			client := NewDeviceFlowClientWithDomainDeviceIDAndProxyURL(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: canary.URL}}, KimiAIDomain, "device", selected)
			client.oauthHost = canary.URL
			if _, errStart := client.RequestDeviceCode(context.Background()); errStart == nil {
				t.Fatal("invalid proxy accepted device request")
			}
			if _, errExchange, _ := client.exchangeDeviceCode(context.Background(), "device"); errExchange == nil {
				t.Fatal("invalid proxy accepted exchange")
			}
			if _, errRefresh := client.RefreshToken(context.Background(), "refresh"); errRefresh == nil {
				t.Fatal("invalid proxy accepted refresh")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid proxy fell back %d times", hits.Load())
	}
}

func TestKimiOAuthExplicitDirectIgnoresGlobalProxy(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"device_code":"direct"}`)
	}))
	defer origin.Close()
	client := NewDeviceFlowClientWithDomainDeviceIDAndProxyURL(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: "invalid://global"}}, KimiAIDomain, "device", "direct")
	client.oauthHost = origin.URL
	if _, errStart := client.RequestDeviceCode(context.Background()); errStart != nil {
		t.Fatal(errStart)
	}
	if hits.Load() != 1 {
		t.Fatal("explicit direct did not reach origin")
	}
}

func TestKimiRefreshSingleflightDoesNotShareDifferentProxyRoutes(t *testing.T) {
	startedA, startedB, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	newProxy := func(name string, started chan struct{}) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": name, "expires_in": 3600})
		}))
	}
	proxyA, proxyB := newProxy("route-a", startedA), newProxy("route-b", startedB)
	t.Cleanup(proxyA.Close)
	t.Cleanup(proxyB.Close)
	results := make(chan string, 2)
	refresh := func(proxy string) {
		client := NewDeviceFlowClientWithDomainDeviceIDAndProxyURL(nil, KimiAIDomain, "same-device", proxy)
		client.oauthHost = "http://auth.kimi.ai"
		token, errRefresh := client.RefreshToken(context.Background(), "shared-refresh")
		if errRefresh != nil {
			results <- fmt.Sprint(errRefresh)
			return
		}
		results <- token.AccessToken
	}
	go refresh(proxyA.URL)
	select {
	case <-startedA:
	case <-time.After(5 * time.Second):
		t.Fatal("route A did not start")
	}
	go refresh(proxyB.URL)
	select {
	case <-startedB:
	case <-time.After(5 * time.Second):
		t.Fatal("route B incorrectly joined another proxy's refresh")
	}
	// Returning releases both requests before server cleanup; both must use their route.
	t.Cleanup(func() {
		seen := map[string]bool{<-results: true, <-results: true}
		if !seen["route-a"] || !seen["route-b"] {
			t.Errorf("refresh routes merged: %v", seen)
		}
	})
}

func TestKimiOAuthProxyErrorsDoNotExposeCredentials(t *testing.T) {
	_, errProxy := NewOAuthHTTPClient("invalid://user:secret@proxy.invalid")
	if errProxy == nil || strings.Contains(errProxy.Error(), "secret") {
		t.Fatalf("unsafe validation error: %v", errProxy)
	}
}
