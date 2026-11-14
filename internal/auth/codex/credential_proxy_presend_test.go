package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestCodexProxyConfigurationFailureReleasesRefreshGrant(t *testing.T) {
	for _, partition := range []string{"outer", "siwc"} {
		t.Run(partition, func(t *testing.T) {
			var fallbackCalls, tokenCalls atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackCalls.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer fallback.Close()
			grant := t.Name()
			endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				tokenCalls.Add(1)
				if err := req.ParseForm(); err != nil || req.Form.Get("refresh_token") != grant {
					t.Errorf("refresh request did not retain its grant: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"recovered-access","refresh_token":"rotated-grant","scope":"chatgpt.tokens.use.direct","expires_in":3600}`)
			}))
			defer endpoint.Close()
			cfg := &config.Config{AuthDir: t.TempDir()}
			cfg.ProxyURL = fallback.URL
			clientID := ClientID
			if partition == "siwc" {
				clientID = "synthetic-issued-client"
			}
			refresh := func(auth *CodexAuth) (string, error) {
				if partition == "siwc" {
					token, err := auth.RefreshSIWCTokens(context.Background(), grant, clientID, "synthetic-device")
					if token != nil {
						return token.AccessToken, err
					}
					return "", err
				}
				token, err := auth.RefreshTokens(context.Background(), grant)
				if token != nil {
					return token.AccessToken, err
				}
				return "", err
			}
			invalid := NewCodexAuthWithProxyURL(cfg, "invalid://synthetic-proxy")
			_, errFirst := refresh(invalid)
			if !IsRefreshPreSendError(errFirst) || IsRefreshOutcomeUnknown(errFirst) {
				t.Fatalf("invalid proxy error = %v, want retryable pre-send failure", errFirst)
			}
			if fallbackCalls.Load() != 0 || tokenCalls.Load() != 0 {
				t.Fatalf("invalid proxy emitted traffic: fallback=%d token=%d", fallbackCalls.Load(), tokenCalls.Load())
			}
			receipt, errRead := invalid.journal(context.Background()).RefreshRead(context.Background(), refreshGrantKey(clientID, grant))
			if errRead != nil || len(receipt) != 0 {
				t.Fatalf("invalid proxy retained a claim: receipt length=%d err=%v", len(receipt), errRead)
			}

			// Redirect only the test endpoint's dial destination. The production
			// constructor still selects explicit direct rather than the fallback.
			repaired := NewCodexAuthWithProxyURL(cfg, "direct")
			repaired.httpClient.Transport = newRedirectedTransport(t, endpoint, nil)
			access, errSecond := refresh(repaired)
			if errSecond != nil || access != "recovered-access" || tokenCalls.Load() != 1 || fallbackCalls.Load() != 0 {
				t.Fatalf("same-grant recovery: access=%q error=%v token=%d fallback=%d", access, errSecond, tokenCalls.Load(), fallbackCalls.Load())
			}
		})
	}
}

func TestCodexUntracedTransportFailureRemainsUnknown(t *testing.T) {
	auth := NewCodexAuthWithProxyURL(nil, "direct")
	auth.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("custom transport failed without proving whether bytes were sent")
	})
	req, err := http.NewRequest(http.MethodPost, TokenURL, strings.NewReader("synthetic-grant"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.doRefreshRequest(req)
	if !IsRefreshOutcomeUnknown(err) || IsRefreshPreSendError(err) {
		t.Fatalf("untraced transport error = %v, want unknown outcome", err)
	}
}
