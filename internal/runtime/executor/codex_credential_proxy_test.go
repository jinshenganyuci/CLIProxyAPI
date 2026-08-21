package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestValidateCodexCredentialProxyPolicy(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: config.CodexCredentialProxyPolicyRequire}}
	tests := []struct {
		name    string
		auth    *cliproxyauth.Auth
		wantErr bool
	}{
		{name: "missing auth", wantErr: true},
		{name: "inherit", auth: codexProxyTestOAuth(""), wantErr: true},
		{name: "invalid", auth: codexProxyTestOAuth("://invalid"), wantErr: true},
		{name: "direct", auth: codexProxyTestOAuth("direct")},
		{name: "http proxy", auth: codexProxyTestOAuth("http://127.0.0.1:8080")},
		{name: "socks proxy", auth: codexProxyTestOAuth("socks5://127.0.0.1:1080")},
		{name: "api key unaffected", auth: &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCodexCredentialProxyPolicy(cfg, test.auth)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateCodexCredentialProxyPolicy() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestCodexCredentialProxyRequireDoesNotFallBackToGlobalProxy(t *testing.T) {
	var globalHits atomic.Int32
	globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		globalHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer globalProxy.Close()

	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{ProxyURL: globalProxy.URL},
		Codex:     config.CodexConfig{CredentialProxyPolicy: config.CodexCredentialProxyPolicyRequire},
	}
	executor := NewCodexExecutor(cfg)
	auth := codexProxyTestOAuth("")
	auth.Attributes = map[string]string{"base_url": "http://127.0.0.1:1"}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "proxy") {
		t.Fatalf("Execute() error = %v, want credential proxy error", errExecute)
	}
	if got := globalHits.Load(); got != 0 {
		t.Fatalf("global proxy received %d request(s), want 0", got)
	}
}

func TestCodexCredentialProxyRequireUnreachableProxyDoesNotFallBack(t *testing.T) {
	var globalHits atomic.Int32
	globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		globalHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
	}))
	defer globalProxy.Close()

	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{ProxyURL: globalProxy.URL},
		Codex:     config.CodexConfig{CredentialProxyPolicy: config.CodexCredentialProxyPolicyRequire},
	}
	auth := codexProxyTestOAuth("http://127.0.0.1:1")
	auth.Attributes = map[string]string{"base_url": "http://upstream.invalid"}
	_, errExecute := NewCodexExecutor(cfg).Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want unreachable credential proxy error")
	}
	if got := globalHits.Load(); got != 0 {
		t.Fatalf("global proxy received %d request(s), want 0", got)
	}
}

func codexProxyTestOAuth(proxyURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "codex-oauth.json",
		Provider: "codex",
		ProxyURL: proxyURL,
		Metadata: map[string]any{"access_token": "secret", "refresh_token": "refresh"},
	}
}
