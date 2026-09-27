package live

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestLiveExplicitInvalidProxyNeverFallsBack(t *testing.T) {
	// ProxyFromEnvironment caches environment settings. A fresh process makes
	// the environment proxy canary deterministic without resetting global state.
	const childFlag = "CPA_LIVE_INVALID_PROXY_TEST_CHILD"
	if os.Getenv(childFlag) != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestLiveExplicitInvalidProxyNeverFallsBack$", "-test.v")
		command.Env = append(os.Environ(), childFlag+"=1")
		output, errRun := command.CombinedOutput()
		if errRun != nil {
			t.Fatalf("isolated invalid proxy test failed: %v\n%s", errRun, output)
		}
		for _, secret := range []string{"proxy-user", "proxy-secret", "proxy.invalid"} {
			if strings.Contains(string(output), secret) {
				t.Fatal("invalid proxy logs exposed proxy authentication or address")
			}
		}
		t.Logf("isolated proxy tests:\n%s", output)
		return
	}

	var directHits, environmentHits, globalHits atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		directHits.Add(1)
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(writer, request, nil)
		if errUpgrade == nil {
			_ = conn.Close()
		}
	}))
	defer direct.Close()
	environmentProxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		environmentHits.Add(1)
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer environmentProxy.Close()
	globalProxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		globalHits.Add(1)
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer globalProxy.Close()
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		t.Setenv(key, environmentProxy.URL)
	}
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(key, "")
	}
	probe, errProbe := http.NewRequest(http.MethodGet, "http://live-proxy-canary.invalid/realtime", nil)
	if errProbe != nil {
		t.Fatal(errProbe)
	}
	proxyURL, errEnvironment := http.ProxyFromEnvironment(probe)
	if errEnvironment != nil || proxyURL == nil || proxyURL.String() != environmentProxy.URL {
		t.Fatal("isolated environment proxy canary was not active")
	}

	invalid := []struct {
		name string
		url  string
	}{
		{"missing_scheme", "invalid-proxy"},
		{"malformed_scheme", "://bad"},
		{"unsupported_scheme", "ftp://proxy-user:proxy-secret@proxy.invalid:1080"},
		{"malformed_host", "socks5://proxy-user:proxy-secret@[bad"},
		{"missing_host", "http://proxy-user:proxy-secret@"},
	}
	for _, test := range invalid {
		for _, source := range []string{"credential", "global"} {
			t.Run(test.name+"/"+source, func(t *testing.T) {
				cfg := &config.Config{}
				cfg.ProxyURL = globalProxy.URL
				selected := &auth.Auth{ID: "synthetic-api-key", Provider: "codex", Attributes: map[string]string{"api_key": "synthetic-key"}}
				if source == "credential" {
					selected.ProxyURL = test.url
				} else {
					cfg.ProxyURL = test.url
				}
				if errPolicy := validateLiveCredentialProxyPolicy(cfg, selected); errPolicy != nil {
					t.Fatalf("API key credential should remain outside the OAuth require policy: %v", errPolicy)
				}
				for _, target := range []string{"ws" + strings.TrimPrefix(direct.URL, "http"), "ws://live-proxy-canary.invalid/realtime"} {
					dialer := newProxyAwareSidebandDialer(cfg, selected)
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					conn, response, errDial := dialer.DialContext(ctx, target, nil)
					cancel()
					if conn != nil {
						_ = conn.Close()
					}
					if response != nil && response.Body != nil {
						_ = response.Body.Close()
					}
					if errDial == nil {
						t.Error("invalid explicit proxy unexpectedly completed a WebSocket handshake")
					} else {
						for _, secret := range []string{"proxy-user", "proxy-secret", "proxy.invalid"} {
							if strings.Contains(errDial.Error(), secret) {
								t.Error("invalid proxy diagnostic exposed proxy authentication or address")
							}
						}
					}
				}
			})
		}
	}
	if directHits.Load() != 0 || environmentHits.Load() != 0 || globalHits.Load() != 0 {
		t.Fatalf("invalid proxy made outbound requests: direct=%d environment=%d global=%d", directHits.Load(), environmentHits.Load(), globalHits.Load())
	}
	t.Run("unset_proxy_keeps_environment_route", func(t *testing.T) {
		dialer := newSidebandDialer("")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, response, errDial := dialer.DialContext(ctx, "ws://live-proxy-canary.invalid/realtime", nil)
		if conn != nil {
			_ = conn.Close()
		}
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if errDial == nil || environmentHits.Load() != 1 || directHits.Load() != 0 || globalHits.Load() != 0 {
			t.Fatalf("unset proxy did not retain the environment route: error=%v direct=%d environment=%d global=%d", errDial, directHits.Load(), environmentHits.Load(), globalHits.Load())
		}
	})
}
