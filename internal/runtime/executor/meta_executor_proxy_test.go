package executor

import (
	"context"
	"encoding/json"
	"fmt"
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
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestMetaExecutorRefreshFromOAuthStorageDoesNotReuseAcquisitionGuard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"api_key": "LLM|new-runtime-key", "base_url": "https://meta-runtime.local.invalid/v1"})
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL+"/key")
	loginContext, cancelLogin := context.WithCancel(context.Background())
	defer cancelLogin()
	storage := &metaauth.MetaTokenStorage{
		Type: "meta", AuthKind: "oauth", AccessToken: "dca:login-token", DCAToken: "dca:login-token",
		DCAExpired: "2099-01-01T00:00:00Z", DCAExpiresAt: 4070908800,
	}
	auth := &cliproxyauth.Auth{
		ID: "meta-login.json", FileName: "meta-login.json", Provider: "meta", Storage: storage,
		Metadata: map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "dca:login-token", "dca_token": "dca:login-token", "prefix": "retained-control"},
	}
	if errBind := metaauth.BindOAuthProxy(auth, "direct", true, loginContext.Err); errBind != nil {
		t.Fatal(errBind)
	}
	directory := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(directory)
	manager := cliproxyauth.NewManager(store, nil, nil)
	auth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	cancelLogin()
	refreshed, errRefresh := NewMetaExecutor(nil).Refresh(context.Background(), auth.Clone())
	if errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if _, ok := refreshed.Storage.(*metaauth.MetaTokenStorage); !ok {
		t.Fatalf("runtime refresh kept acquisition storage: %T", refreshed.Storage)
	}
	if _, errUpdate := manager.UpdateRefreshedAuth(context.Background(), auth, refreshed); errUpdate != nil {
		t.Fatalf("runtime refresh reused a cancelled login guard: %v", errUpdate)
	}
	raw, errRead := os.ReadFile(filepath.Join(directory, auth.FileName))
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved map[string]any
	if errDecode := json.Unmarshal(raw, &saved); errDecode != nil {
		t.Fatal(errDecode)
	}
	if saved["api_key"] != "LLM|new-runtime-key" || saved["access_token"] != "LLM|new-runtime-key" || saved["dca_token"] != "dca:login-token" || saved["dca_expired"] != "2099-01-01T00:00:00Z" || saved["proxy_url"] != "direct" || saved["prefix"] != "retained-control" {
		t.Fatal("persisted runtime refresh lost minted tokens, DCA lifecycle, selected proxy or controls")
	}
	if storage.APIKey != "" || storage.AccessToken != "dca:login-token" || auth.Metadata["access_token"] != "dca:login-token" {
		t.Fatal("runtime candidate changed the acquisition storage before manager acceptance")
	}
}

func TestMetaExecutorSelectedProxyLifecycle(t *testing.T) {
	for _, operation := range []string{"execute", "stream", "http_request", "refresh", "execute_lazy_mint"} {
		t.Run(operation, func(t *testing.T) {
			for _, route := range []string{"credential", "global", "direct", "direct_over_invalid_global", "invalid_credential", "invalid_global", "unreachable_credential"} {
				t.Run(route, func(t *testing.T) {
					var directCount, selectedCount, globalCount, contextCount atomic.Int32
					serve := func(count *atomic.Int32, label string) http.HandlerFunc {
						return func(w http.ResponseWriter, r *http.Request) {
							count.Add(1)
							if r.URL.Path == "/key" {
								if r.Header.Get("Authorization") != "Bearer dca:local-proxy-test" {
									t.Error("mint did not receive the selected credential's DCA token")
								}
								w.Header().Set("Content-Type", "application/json")
								_ = json.NewEncoder(w).Encode(map[string]string{"api_key": "LLM|minted-local-key"})
								return
							}
							if token := r.Header.Get("Authorization"); token != "Bearer LLM|local-key" && token != "Bearer LLM|minted-local-key" {
								t.Error("inference did not receive the selected credential's API key")
							}
							if operation == "http_request" && r.Header.Get("X-Client-Id") != "tbh:tui" {
								t.Error("prepared HTTP request is missing the valid Meta client header")
							}
							writeMetaResponsesOK(w, label)
						}
					}
					direct := httptest.NewServer(serve(&directCount, "direct"))
					defer direct.Close()
					selected := httptest.NewServer(serve(&selectedCount, "selected"))
					defer selected.Close()
					global := httptest.NewServer(serve(&globalCount, "global"))
					defer global.Close()
					injected := httptest.NewServer(serve(&contextCount, "context"))
					defer injected.Close()
					injectedURL, errURL := url.Parse(injected.URL)
					if errURL != nil {
						t.Fatal(errURL)
					}
					transport := &http.Transport{Proxy: http.ProxyURL(injectedURL)}
					defer transport.CloseIdleConnections()
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
					t.Setenv("META_MINT_URL", direct.URL+"/key")
					cfg := &config.Config{}
					cfg.ProxyURL = global.URL
					auth := &cliproxyauth.Auth{
						ID: "local-proxy-test", Provider: "meta", ProxyURL: selected.URL,
						Metadata: map[string]any{"api_key": "LLM|local-key", "base_url": direct.URL, "dca_token": "dca:local-proxy-test"},
					}
					wantDirect, wantSelected, wantGlobal := int32(0), int32(0), int32(0)
					wantError := false
					switch route {
					case "credential":
						wantSelected = 1
					case "global":
						auth.ProxyURL = ""
						wantGlobal = 1
					case "direct", "direct_over_invalid_global":
						auth.ProxyURL = "direct"
						wantDirect = 1
						if route == "direct_over_invalid_global" {
							cfg.ProxyURL = "invalid-proxy"
						}
					case "invalid_credential":
						auth.ProxyURL = "unsupported://user:secret@127.0.0.1:1"
						wantError = true
					case "invalid_global":
						auth.ProxyURL = ""
						cfg.ProxyURL = "missing-proxy-scheme"
						wantError = true
					case "unreachable_credential":
						listener, errListen := net.Listen("tcp", "127.0.0.1:0")
						if errListen != nil {
							t.Fatal(errListen)
						}
						auth.ProxyURL = "http://" + listener.Addr().String()
						if errClose := listener.Close(); errClose != nil {
							t.Fatal(errClose)
						}
						wantError = true
					}
					executor := NewMetaExecutor(cfg)
					request := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"local proxy fixture"}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
					var err error
					switch operation {
					case "execute", "execute_lazy_mint":
						if operation == "execute_lazy_mint" {
							delete(auth.Metadata, "api_key")
							wantDirect *= 2
							wantSelected *= 2
							wantGlobal *= 2
						}
						_, err = executor.Execute(ctx, auth, request, opts)
					case "stream":
						var result *cliproxyexecutor.StreamResult
						result, err = executor.ExecuteStream(ctx, auth, request, opts)
						if err == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									err = chunk.Err
								}
							}
						}
					case "http_request":
						httpRequest, errRequest := http.NewRequest(http.MethodGet, direct.URL+"/responses", nil)
						if errRequest != nil {
							t.Fatal(errRequest)
						}
						var response *http.Response
						response, err = executor.HttpRequest(ctx, auth, httpRequest)
						if response != nil {
							_, _ = io.Copy(io.Discard, response.Body)
							if errClose := response.Body.Close(); errClose != nil {
								t.Error(errClose)
							}
						}
					case "refresh":
						_, err = executor.Refresh(ctx, auth)
					}
					if (err != nil) != wantError {
						t.Fatalf("error = %v, want error = %v", err, wantError)
					}
					if err != nil && strings.Contains(err.Error(), "secret") {
						t.Fatal("proxy error exposed the selected proxy password")
					}
					if directCount.Load() != wantDirect || selectedCount.Load() != wantSelected || globalCount.Load() != wantGlobal || contextCount.Load() != 0 {
						t.Fatalf("route counts direct/selected/global/context = %d/%d/%d/%d, want %d/%d/%d/0", directCount.Load(), selectedCount.Load(), globalCount.Load(), contextCount.Load(), wantDirect, wantSelected, wantGlobal)
					}
				})
			}
		})
	}
}

func TestMetaExecutorMintDoesNotCoalesceAcrossCredentialOrProxy(t *testing.T) {
	for _, dimension := range []string{"proxy", "credential"} {
		t.Run(dimension, func(t *testing.T) {
			firstStarted := make(chan struct{})
			releaseFirst := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
			var requests atomic.Int32
			serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				number := requests.Add(1)
				if number == 1 {
					close(firstStarted)
					<-releaseFirst
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"api_key": fmt.Sprintf("LLM|key-%d", number)})
			})
			firstProxy := httptest.NewServer(serve)
			defer firstProxy.Close()
			secondProxy := httptest.NewServer(serve)
			defer secondProxy.Close()
			defer release()
			t.Setenv("META_MINT_URL", "http://meta-mint.local.invalid/key")
			executor := NewMetaExecutor(nil)
			firstAuth := &cliproxyauth.Auth{ID: "same-credential", Provider: "meta", ProxyURL: firstProxy.URL, Metadata: map[string]any{"dca_token": "dca:same-token"}}
			secondAuth := firstAuth.Clone()
			if dimension == "proxy" {
				secondAuth.ProxyURL = secondProxy.URL
			} else {
				secondAuth.ID = "other-credential"
			}
			type result struct {
				auth *cliproxyauth.Auth
				err  error
			}
			firstResult := make(chan result, 1)
			secondResult := make(chan result, 1)
			go func() {
				auth, err := executor.Refresh(context.Background(), firstAuth)
				firstResult <- result{auth: auth, err: err}
			}()
			select {
			case <-firstStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("first mint did not reach its selected proxy")
			}
			go func() {
				auth, err := executor.Refresh(context.Background(), secondAuth)
				secondResult <- result{auth: auth, err: err}
			}()
			select {
			case second := <-secondResult:
				if second.err != nil || second.auth == nil || second.auth.Metadata["api_key"] != "LLM|key-2" {
					t.Errorf("second mint did not independently use its credential and proxy: %v", second.err)
				}
			case <-time.After(5 * time.Second):
				t.Error("second mint incorrectly waited for a different credential or proxy")
			}
			release()
			first := <-firstResult
			if first.err != nil || first.auth == nil || first.auth.Metadata["api_key"] != "LLM|key-1" {
				t.Errorf("first mint failed: %v", first.err)
			}
			if requests.Load() != 2 {
				t.Errorf("mint requests = %d, want 2", requests.Load())
			}
		})
	}
}
