package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDevinAuthenticatorProviderAndRefreshLead(t *testing.T) {
	authenticator := NewDevinAuthenticator()
	if authenticator.Provider() != "devin" {
		t.Fatalf("Provider() = %q, want devin", authenticator.Provider())
	}
	lead := authenticator.RefreshLead()
	if lead != nil {
		t.Fatalf("RefreshLead() = %v, want nil for permanent tokens", lead)
	}
}

func TestDevinAuthenticatorHeadlessManualTokenLogin(t *testing.T) {
	fixture := newDevinLoginProxyFixture(t)
	authenticator := NewDevinAuthenticator()
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://127.0.0.1:1"}}

	mockPrompt := func(prompt string) (string, error) {
		return "devin-session-token$eyJmock.session.token", nil
	}

	opts := &LoginOptions{
		NoBrowser: true,
		Prompt:    mockPrompt,
		Metadata:  map[string]string{"proxy_url": fixture.proxy.URL},
	}

	auth, err := authenticator.Login(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	if auth.Provider != "devin" {
		t.Errorf("auth.Provider = %q, want devin", auth.Provider)
	}
	if auth.Attributes["api_key"] != "devin-session-token$eyJmock.session.token" {
		t.Errorf("api_key = %q, want expected", auth.Attributes["api_key"])
	}
	if auth.ProxyURL != fixture.proxy.URL || auth.Metadata["proxy_url"] != fixture.proxy.URL {
		t.Error("manual token login did not persist its explicit proxy")
	}
}
