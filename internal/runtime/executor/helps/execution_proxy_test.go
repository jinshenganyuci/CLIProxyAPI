package helps

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
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

func TestExecutionProxyHTTPClientsRespectCredentialBinding(t *testing.T) {
	factories := map[string]func(context.Context, *config.Config, *coreauth.Auth) (*http.Client, error){
		"standard": func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*http.Client, error) {
			return NewProxyAwareHTTPClient(ctx, cfg, auth, 0), nil
		},
		"uTLS standard host": func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*http.Client, error) {
			return NewUtlsHTTPClient(ctx, cfg, auth, 0), nil
		},
		"Devin": func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*http.Client, error) {
			return NewDevinHTTPClient(ctx, cfg, auth, 0)
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			for _, test := range []struct {
				name, credential, request, want string
				reject                          bool
			}{
				{name: "configured", credential: "credential", want: "credential"},
				{name: "matching", credential: "credential", request: "credential", want: "credential"},
				{name: "conflicting", credential: "credential", request: "request", reject: true},
				{name: "direct protected", credential: "direct", request: "request", reject: true},
				{name: "direct same", credential: "direct", request: "direct", want: "origin"},
				{name: "unbound override", request: "request", want: "request"},
				{name: "unbound global", want: "global"},
				{name: "invalid override bound", credential: "credential", request: "invalid://user:secret@proxy.example", reject: true},
				{name: "invalid override unbound", request: "invalid://user:secret@proxy.example", reject: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					counters := map[string]*atomic.Int32{}
					urls := map[string]string{"direct": "direct", "": ""}
					for _, route := range []string{"origin", "credential", "request", "global"} {
						counter := &atomic.Int32{}
						counters[route] = counter
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { counter.Add(1); _, _ = io.WriteString(w, route) }))
						t.Cleanup(server.Close)
						urls[route] = server.URL
					}
					resolve := func(value string) string {
						if u, ok := urls[value]; ok {
							return u
						}
						return value
					}
					var contextHits atomic.Int32
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) { contextHits.Add(1); return nil, io.ErrUnexpectedEOF }))
					ctx = coreexecutor.WithRequestProxyURL(ctx, resolve(test.request))
					client, errCall := factory(ctx, &config.Config{SDKConfig: config.SDKConfig{ProxyURL: urls["global"]}}, &coreauth.Auth{ProxyURL: resolve(test.credential)})
					var responseBody string
					if errCall == nil {
						if client.Timeout != 0 {
							t.Fatal("inference client added timeout")
						}
						var response *http.Response
						response, errCall = client.Get(urls["origin"])
						if response != nil {
							body, _ := io.ReadAll(response.Body)
							_ = response.Body.Close()
							responseBody = string(body)
						}
					}
					if test.reject {
						var policy *proxyutil.ExecutionProxyError
						if !errors.As(errCall, &policy) || !policy.IsRequestScoped() {
							t.Fatalf("not typed request rejection: %v", errCall)
						}
					} else if errCall != nil || responseBody != test.want {
						t.Fatalf("response=%q err=%v want=%q", responseBody, errCall, test.want)
					}
					for route, counter := range counters {
						want := int32(0)
						if !test.reject && route == test.want {
							want = 1
						}
						if counter.Load() != want {
							t.Fatalf("route %s got %d hits want%d", route, counter.Load(), want)
						}
					}
					if contextHits.Load() != 0 {
						t.Fatal("configured route used context fallback")
					}
				})
			}
		})
	}
}

func TestExecutionProxyUTLSProtectedHostsValidateBeforeDial(t *testing.T) {
	for _, host := range []string{"api.anthropic.com", "chatgpt.com"} {
		t.Run(host, func(t *testing.T) {
			var credentialHits, requestHits atomic.Int32
			credential := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				credentialHits.Add(1)
				if r.Method != http.MethodConnect {
					t.Error("expected HTTPS proxy CONNECT")
				}
				http.Error(w, "fixture reject", 502)
			}))
			defer credential.Close()
			request := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requestHits.Add(1); http.Error(w, "fixture reject", 502) }))
			defer request.Close()
			auth := &coreauth.Auth{ProxyURL: credential.URL}
			conflict := coreexecutor.WithRequestProxyURL(context.Background(), request.URL)
			_, errConflict := NewUtlsHTTPClient(conflict, nil, auth, 0).Get("https://" + host + "/v1")
			var policy *proxyutil.ExecutionProxyError
			if !errors.As(errConflict, &policy) {
				t.Fatalf("expected typed error: %v", errConflict)
			}
			if credentialHits.Load() != 0 || requestHits.Load() != 0 {
				t.Fatal("conflicting request reached proxy")
			}
			matching := coreexecutor.WithRequestProxyURL(context.Background(), credential.URL)
			_, errMatching := NewUtlsHTTPClient(matching, nil, auth, 0).Get("https://" + host + "/v1")
			if errMatching == nil || errors.As(errMatching, &policy) {
				t.Fatalf("expected fixture connection rejection: %v", errMatching)
			}
			if credentialHits.Load() != 1 || requestHits.Load() != 0 {
				t.Fatal("matching request did not use credential proxy")
			}
			if strings.Contains(errConflict.Error(), request.URL) {
				t.Fatal("policy error exposed proxy URL")
			}
		})
	}
}
