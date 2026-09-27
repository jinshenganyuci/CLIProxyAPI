package helps

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestNewDevinHTTPClientRejectsSelectedInvalidRoute(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid selected route reached the injected transport")
		return nil, nil
	}))
	for _, raw := range []string{"proxy.invalid:1080", "ftp://proxy.invalid:1080", "http://user:secret@", "http://%invalid"} {
		for _, source := range []string{"credential", "global"} {
			t.Run(source+"/"+strings.Split(raw, ":")[0], func(t *testing.T) {
				cfg := &config.Config{}
				auth := &cliproxyauth.Auth{}
				if source == "credential" {
					auth.ProxyURL = raw
					cfg.ProxyURL = "direct"
				} else {
					cfg.ProxyURL = raw
				}
				client, errClient := NewDevinHTTPClient(ctx, cfg, auth, 0)
				if errClient == nil || client != nil {
					t.Fatalf("invalid selected route returned client %v, error %v", client, errClient)
				}
				if strings.Contains(errClient.Error(), "secret") || strings.Contains(errClient.Error(), "user:") {
					t.Fatal("proxy error exposed credentials")
				}
			})
		}
	}
}

func TestNewDevinHTTPClientCachesBySelectedRoute(t *testing.T) {
	auth := &cliproxyauth.Auth{ProxyURL: "  http://credential-one.invalid:8080  "}
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global.invalid:8080"}}
	first, errFirst := NewDevinHTTPClient(context.Background(), cfg, auth, 0)
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	second, errSecond := NewDevinHTTPClient(context.Background(), nil, &cliproxyauth.Auth{ProxyURL: strings.TrimSpace(auth.ProxyURL)}, 30*time.Second)
	if errSecond != nil {
		t.Fatal(errSecond)
	}
	if first.Transport != second.Transport {
		t.Fatal("same selected route did not reuse the cached transport")
	}
	if first.Timeout != 0 || second.Timeout != 30*time.Second {
		t.Fatalf("client timeout changed: inference %v, acquisition %v", first.Timeout, second.Timeout)
	}
	third, errThird := NewDevinHTTPClient(context.Background(), cfg, &cliproxyauth.Auth{ProxyURL: "http://credential-two.invalid:8080"}, 0)
	if errThird != nil {
		t.Fatal(errThird)
	}
	if first.Transport == third.Transport {
		t.Fatal("different credential routes share a transport")
	}
	for _, client := range []*http.Client{first, second, third} {
		transport, ok := client.Transport.(*http.Transport)
		if !ok || !transport.DisableCompression {
			t.Fatalf("transport %T does not disable automatic gzip", client.Transport)
		}
	}
}

func TestNewDevinHTTPClientExplicitDirectBypassesAllOtherRoutes(t *testing.T) {
	for _, raw := range []string{"direct", "none"} {
		t.Run(raw, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			})
			cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "invalid global route"}}
			client, errClient := NewDevinHTTPClient(ctx, cfg, &cliproxyauth.Auth{ProxyURL: raw}, 0)
			if errClient != nil {
				t.Fatal(errClient)
			}
			tr, ok := client.Transport.(*http.Transport)
			if !ok || tr.Proxy != nil {
				t.Fatal("explicit direct retained a proxy resolver")
			}
		})
	}
}

func TestNewDevinHTTPClientContextTransportClonePreservesOriginal(t *testing.T) {
	base := &http.Transport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", base)
	client, errClient := NewDevinHTTPClient(ctx, nil, nil, 0)
	if errClient != nil {
		t.Fatal(errClient)
	}
	if client.Transport == base || base.DisableCompression {
		t.Fatal("Devin mutated the shared injected transport")
	}
}

func TestNewDevinHTTPClientCustomTransportPreservesRequest(t *testing.T) {
	var received *http.Request
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		received = req
		return &http.Response{StatusCode: http.StatusOK}, nil
	}))
	client, errClient := NewDevinHTTPClient(ctx, nil, nil, 0)
	if errClient != nil {
		t.Fatal(errClient)
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	if _, errRoundTrip := client.Transport.RoundTrip(req); errRoundTrip != nil {
		t.Fatal(errRoundTrip)
	}
	if req.Header.Get("Accept-Encoding") != "" || received.Header.Get("Accept-Encoding") != "identity" {
		t.Fatal("custom transport did not suppress gzip without mutating the original request")
	}
	req.Header.Set("Accept-Encoding", "identity")
	if _, errRoundTrip := client.Transport.RoundTrip(req); errRoundTrip != nil {
		t.Fatal(errRoundTrip)
	}
	if received.Header.Get("Accept-Encoding") != "identity" {
		t.Fatal("custom transport changed an explicit Accept-Encoding header")
	}
}
