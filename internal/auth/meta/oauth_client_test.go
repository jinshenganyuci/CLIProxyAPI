package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestMetaOAuthInvalidProxyFailsBeforeAnyAcquisitionRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	for _, proxyURL := range []string{"broken://user:secret@proxy.example", "://secret"} {
		svc := NewMetaAuthWithProxyURL(&config.Config{}, proxyURL).WithHTTPClient(server.Client())
		for _, run := range []func() error{
			func() error { _, err := svc.StartDeviceFlowWithEndpoint(context.Background(), server.URL); return err },
			func() error {
				_, err := svc.WaitForAuthorization(context.Background(), &DeviceCodeResponse{DeviceCode: "synthetic", TokenEndpoint: server.URL})
				return err
			},
			func() error { _, err := svc.MintAPIKey(context.Background(), "dca:synthetic"); return err },
		} {
			if err := run(); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid route was accepted or exposed")
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid proxy made a network request")
	}
}

func TestMetaOAuthClientFreezesSelectedRouteAcrossDevicePollAndMint(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device":
			_ = json.NewEncoder(w).Encode(DeviceCodeResponse{DeviceCode: "device", UserCode: "USER", Interval: 1, ExpiresIn: 5})
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("device_code") != "device" || r.Form.Get("grant_type") != DeviceCodeGrantType {
				t.Error("token poll omitted device credentials")
			}
			_ = json.NewEncoder(w).Encode(TokenData{AccessToken: "dca:synthetic", TokenType: "Bearer", ExpiresIn: 60})
		case "/mint":
			if r.Header.Get("Authorization") != "Bearer dca:synthetic" {
				t.Error("mint did not use acquired DCA token")
			}
			_ = json.NewEncoder(w).Encode(MintedKeyResponse{APIKey: "LLM|synthetic", UserEmail: "local@example.test"})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer proxy.Close()
	var canaryCalls atomic.Int32
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { canaryCalls.Add(1) }))
	defer canary.Close()
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: proxy.URL}}
	svc := NewMetaAuth(cfg)
	svc.SetMintURL(canary.URL + "/mint")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	device, err := svc.StartDeviceFlowWithEndpoint(ctx, canary.URL+"/device")
	if err != nil {
		t.Fatal(err)
	}
	device.TokenEndpoint = canary.URL + "/token"
	// A live configuration change must not move an in-flight device grant.
	cfg.ProxyURL = canary.URL
	bundle, errWait := svc.WaitForAuthorization(ctx, device)
	if errWait != nil {
		t.Fatal(errWait)
	}
	storage := svc.CreateTokenStorage(bundle)
	if storage.APIKey != "LLM|synthetic" || storage.DCAToken != "dca:synthetic" || storage.Expired != "" || storage.DCAExpired == "" {
		t.Fatal("native DCA and minted-key lifetime semantics changed")
	}
	mu.Lock()
	got := strings.Join(paths, ",")
	mu.Unlock()
	if got != "/device,/token,/mint" || canaryCalls.Load() != 0 {
		t.Fatalf("acquisition changed its route: paths=%s direct=%d", got, canaryCalls.Load())
	}
}

func TestMetaOAuthDirectBypassesConfiguredAndEnvironmentProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{DeviceCode: "direct", UserCode: "DIRECT"})
	}))
	defer server.Close()
	svc := NewMetaAuthWithProxyURL(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: proxy.URL}}, "direct")
	transport := svc.httpClient.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("direct transport retains an environment proxy resolver")
	}
	if _, err := svc.StartDeviceFlowWithEndpoint(context.Background(), server.URL); err != nil {
		t.Fatal(err)
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("direct credential used a proxy")
	}
}

func TestMetaOAuthAcquisitionErrorsDoNotExposeResponseTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"access_token":"upstream-secret","error":"upstream-secret"}`))
	}))
	defer server.Close()
	svc := NewMetaAuth(nil)
	svc.SetMintURL(server.URL)
	for _, run := range []func() error{
		func() error { _, err := svc.StartDeviceFlowWithEndpoint(context.Background(), server.URL); return err },
		func() error { _, err := svc.MintAPIKey(context.Background(), "dca:local"); return err },
		func() error {
			_, err := svc.WaitForAuthorization(context.Background(), &DeviceCodeResponse{DeviceCode: "local", TokenEndpoint: server.URL, Interval: 1, ExpiresIn: 5})
			return err
		},
	} {
		if err := run(); err == nil || strings.Contains(err.Error(), "upstream-secret") {
			t.Fatal("OAuth error exposed an upstream credential")
		}
	}
}
