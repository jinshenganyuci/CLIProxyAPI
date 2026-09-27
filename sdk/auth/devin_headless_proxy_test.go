package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestDevinAuthenticatorHeadlessInjectedServiceUsesFrozenProxy(t *testing.T) {
	var directRequests atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(direct.Close)
	var changedRequests atomic.Int32
	changed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		changedRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(changed.Close)

	var mu sync.Mutex
	var paths []string
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if !r.URL.IsAbs() || r.URL.Scheme+"://"+r.URL.Host != direct.URL {
			t.Errorf("request did not preserve the injected endpoint through the selected proxy: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		serveDevinHeadlessAcquisition(t, w, r)
	}))
	t.Cleanup(selected.Close)

	service := devinauth.NewDevinAuthService(direct.Client())
	service.SetAPIBaseURL(direct.URL)
	service.SetServerBaseURL(direct.URL)
	service.SetAppBaseURL(direct.URL)
	authenticator := NewDevinAuthenticator()
	authenticator.AuthService = service
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: changed.URL}}
	opts := &LoginOptions{NoBrowser: true, Metadata: map[string]string{"proxy_url": selected.URL}}
	opts.Prompt = func(string) (string, error) {
		cfg.ProxyURL = changed.URL
		opts.Metadata["proxy_url"] = changed.URL
		service.SetAPIBaseURL(changed.URL)
		service.SetServerBaseURL(changed.URL)
		service.SetAppBaseURL(changed.URL)
		return "headless-code", nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var recordProxy string
	printed := captureStdout(t, func() {
		record, errLogin := authenticator.Login(ctx, cfg, opts)
		if errLogin != nil {
			t.Fatalf("headless proxy acquisition failed: %v", errLogin)
		}
		recordProxy = record.ProxyURL
		if record.Metadata["proxy_url"] != selected.URL || record.Storage == nil {
			t.Fatal("headless credential lost its selected proxy or guarded storage")
		}
		path := filepath.Join(t.TempDir(), record.FileName)
		if errSave := record.Storage.SaveTokenToFile(path); errSave != nil {
			t.Fatalf("save headless credential: %v", errSave)
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		var saved map[string]any
		if errDecode := json.Unmarshal(data, &saved); errDecode != nil {
			t.Fatal(errDecode)
		}
		if saved["proxy_url"] != selected.URL || saved["session_token"] != "devin-session-token$eyJheadless.token" {
			t.Fatal("saved credential did not preserve the acquisition token and proxy")
		}
		cancel()
		canceledPath := filepath.Join(t.TempDir(), record.FileName)
		if errSave := record.Storage.SaveTokenToFile(canceledPath); !errors.Is(errSave, context.Canceled) {
			t.Fatalf("canceled acquisition storage error = %v, want context cancellation", errSave)
		}
		if _, errStat := os.Stat(canceledPath); !os.IsNotExist(errStat) {
			t.Fatalf("canceled acquisition persisted a credential: %v", errStat)
		}
	})
	if recordProxy != selected.URL {
		t.Fatal("headless credential proxy changed during acquisition")
	}
	if !strings.Contains(printed, direct.URL+"/auth/cli/continue?") || strings.Contains(printed, "redirect_uri=") {
		t.Fatal("headless authorization URL lost its endpoint snapshot or requires a callback")
	}
	mu.Lock()
	gotPaths := strings.Join(paths, ",")
	mu.Unlock()
	if gotPaths != "/auth/cli/token,/v3/self,"+devinauth.DevinGetUserStatusPath {
		t.Fatalf("selected proxy acquisition paths = %q", gotPaths)
	}
	if directRequests.Load() != 0 || changedRequests.Load() != 0 {
		t.Fatal("headless acquisition escaped the selected proxy or frozen endpoints")
	}
}

func TestDevinAuthenticatorHeadlessDirectReplacesInjectedTransport(t *testing.T) {
	var directRequests atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directRequests.Add(1)
		serveDevinHeadlessAcquisition(t, w, r)
	}))
	t.Cleanup(direct.Close)
	var fallbackRequests atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(fallback.Close)
	fallbackURL, errURL := url.Parse(fallback.URL)
	if errURL != nil {
		t.Fatal(errURL)
	}
	injectedTransport := &http.Transport{Proxy: http.ProxyURL(fallbackURL)}
	t.Cleanup(injectedTransport.CloseIdleConnections)
	service := devinauth.NewDevinAuthService(&http.Client{Transport: injectedTransport})
	service.SetAPIBaseURL(direct.URL)
	service.SetServerBaseURL(direct.URL)
	authenticator := NewDevinAuthenticator()
	authenticator.AuthService = service
	record, errLogin := authenticator.Login(context.Background(), &config.Config{
		SDKConfig: config.SDKConfig{ProxyURL: fallback.URL},
	}, &LoginOptions{
		NoBrowser: true,
		Metadata:  map[string]string{"proxy_url": "direct"},
		Prompt:    func(string) (string, error) { return "headless-code", nil },
	})
	if errLogin != nil {
		t.Fatalf("explicit direct login failed: %v", errLogin)
	}
	if record.ProxyURL != "direct" || record.Metadata["proxy_url"] != "direct" || record.Storage == nil {
		t.Fatal("direct acquisition did not bind a direct credential")
	}
	if directRequests.Load() != 3 || fallbackRequests.Load() != 0 {
		t.Fatalf("direct request count = %d, fallback request count = %d", directRequests.Load(), fallbackRequests.Load())
	}
}

func TestDevinAuthenticatorHeadlessBrokenProxyDoesNotUseInjectedClient(t *testing.T) {
	var directRequests atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directRequests.Add(1)
		serveDevinHeadlessAcquisition(t, w, r)
	}))
	t.Cleanup(direct.Close)
	var proxyRequests atomic.Int32
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(broken.Close)
	service := devinauth.NewDevinAuthService(direct.Client())
	service.SetAPIBaseURL(direct.URL)
	service.SetServerBaseURL(direct.URL)
	authenticator := NewDevinAuthenticator()
	authenticator.AuthService = service
	record, errLogin := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{
		NoBrowser: true,
		Metadata:  map[string]string{"proxy_url": broken.URL},
		Prompt:    func(string) (string, error) { return "headless-code", nil },
	})
	if errLogin == nil || record != nil {
		t.Fatal("broken selected proxy unexpectedly produced a credential")
	}
	if proxyRequests.Load() != 1 || directRequests.Load() != 0 {
		t.Fatalf("broken proxy request count = %d, escaped direct requests = %d", proxyRequests.Load(), directRequests.Load())
	}
}

func TestDevinAuthenticatorHeadlessDoesNotListenOnCallbackPort(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	t.Cleanup(func() { _ = listener.Close() })
	fixture := newDevinLoginProxyFixture(t)
	authenticator := NewDevinAuthenticator()
	port := listener.Addr().(*net.TCPAddr).Port
	authenticator.CallbackPort = port
	record, errLogin := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{
		NoBrowser:    true,
		CallbackPort: port,
		Metadata:     map[string]string{"proxy_url": fixture.proxy.URL},
		Prompt:       func(string) (string, error) { return "devin-session-token$eyJmock.session.token", nil },
	})
	if errLogin != nil || record == nil {
		t.Fatalf("headless login failed with an occupied callback port: %v", errLogin)
	}
	if record.ProxyURL != fixture.proxy.URL || record.Storage == nil {
		t.Fatal("callback-free login bypassed proxy binding")
	}
}

func serveDevinHeadlessAcquisition(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	switch r.URL.Path {
	case "/auth/cli/token":
		var payload map[string]string
		if errDecode := json.NewDecoder(r.Body).Decode(&payload); errDecode != nil {
			t.Errorf("decode exchange request: %v", errDecode)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload["code"] != "headless-code" || payload["code_verifier"] == "" {
			t.Error("headless exchange lost its authorization code or PKCE verifier")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"token":"eyJheadless.token"}`)
	case "/v3/self":
		_, _ = io.WriteString(w, `{"user_name":"headless-test","user_id":"headless-test-id"}`)
	case devinauth.DevinGetUserStatusPath:
		_, _ = w.Write([]byte{0x0a, 0x00})
	default:
		t.Errorf("unexpected headless OAuth endpoint: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}
