package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestRefreshTokensKeepsDifferentProxyRoutesSeparateForDistinctGrants(t *testing.T) {
	authA := NewCodexAuthWithProxyURL(nil, "http://proxy-a.invalid:8080")
	authB := NewCodexAuthWithProxyURL(nil, "http://proxy-b.invalid:8080")
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	var workers sync.WaitGroup
	defer func() {
		releaseOnce.Do(func() { close(release) })
		workers.Wait()
	}()
	type refreshResult struct {
		want string
		data *CodexTokenData
		err  error
	}
	results := make(chan refreshResult, 2)
	for index, auth := range []*CodexAuth{authA, authB} {
		want := []string{"route-a-access", "route-b-access"}[index]
		auth.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			started <- struct{}{}
			<-release
			return codexProxyRefreshTestResponse(request, want), nil
		})
		workers.Add(1)
		go func() {
			defer workers.Done()
			data, errRefresh := auth.RefreshTokens(context.Background(), t.Name()+"/"+want)
			results <- refreshResult{want: want, data: data, err: errRefresh}
		}()
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for range 2 {
		select {
		case <-started:
		case <-timer.C:
			t.Fatal("different selected proxies merged into one token refresh")
		}
	}
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		result := <-results
		if result.err != nil || result.data == nil || result.data.AccessToken != result.want {
			t.Fatalf("refresh result = %#v, error = %v, want own route %q", result.data, result.err, result.want)
		}
	}
}

func TestRefreshTokensDifferentProxyCannotReplayPendingGrant(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	authA := NewCodexAuthWithProxyURL(cfg, "http://proxy-a.invalid:8080")
	authB := NewCodexAuthWithProxyURL(cfg, "http://proxy-b.invalid:8080")
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var workers sync.WaitGroup
	defer func() {
		releaseOnce.Do(func() { close(release) })
		workers.Wait()
	}()
	authA.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return codexProxyRefreshTestResponse(request, "route-a-access"), nil
	})
	var routeBCalls atomic.Int32
	authB.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		routeBCalls.Add(1)
		return codexProxyRefreshTestResponse(request, "route-b-access"), nil
	})
	type refreshResult struct {
		data *CodexTokenData
		err  error
	}
	first := make(chan refreshResult, 1)
	second := make(chan refreshResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		data, errRefresh := authA.RefreshTokens(context.Background(), t.Name())
		first <- refreshResult{data: data, err: errRefresh}
	}()
	<-started
	workers.Add(1)
	go func() {
		defer workers.Done()
		data, errRefresh := authB.RefreshTokens(context.Background(), t.Name())
		second <- refreshResult{data: data, err: errRefresh}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case result := <-second:
		if result.data != nil || !IsRefreshOutcomeUnknown(result.err) || routeBCalls.Load() != 0 {
			t.Fatalf("pending grant crossed proxy routes: data=%#v error=%v calls=%d", result.data, result.err, routeBCalls.Load())
		}
	case <-timer.C:
		t.Fatal("different proxy joined the first route's in-flight refresh")
	}
	releaseOnce.Do(func() { close(release) })
	result := <-first
	if result.err != nil || result.data == nil || result.data.AccessToken != "route-a-access" {
		t.Fatalf("original route refresh: data=%#v error=%v", result.data, result.err)
	}
}

func TestRefreshTokensDifferentProxyRecoversCompletedGrantWithoutEgress(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	authA := NewCodexAuthWithProxyURL(cfg, "http://proxy-a.invalid:8080")
	authA.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return codexProxyRefreshTestResponse(request, "recovered-access"), nil
	})
	first, errFirst := authA.RefreshTokens(context.Background(), t.Name())
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	// A new caller on another route may recover the durable receipt for the
	// same consumed grant, but cannot issue that grant to the provider again.
	authB := NewCodexAuthWithProxyURL(cfg, "http://proxy-b.invalid:8080")
	var routeBCalls atomic.Int32
	authB.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		routeBCalls.Add(1)
		return codexProxyRefreshTestResponse(request, "unexpected-route-b-access"), nil
	})
	recovered, errRecovered := authB.RefreshTokens(context.Background(), t.Name())
	if errRecovered != nil || recovered == nil || recovered.AccessToken != first.AccessToken || recovered.RefreshToken != first.RefreshToken || routeBCalls.Load() != 0 {
		t.Fatalf("receipt recovery: data=%#v error=%v calls=%d", recovered, errRecovered, routeBCalls.Load())
	}
	storage := &CodexTokenStorage{
		IdentityVersion:   CredentialIdentityCurrentVersion,
		IdentityNamespace: "11111111-1111-4111-8111-111111111111",
		Metadata: map[string]any{
			"proxy_url":   "http://proxy-b.invalid:8080",
			"plugin_data": "preserved",
		},
	}
	authB.UpdateTokenStorage(storage, recovered)
	if storage.IdentityNamespace != "11111111-1111-4111-8111-111111111111" || storage.IdentityVersion != CredentialIdentityCurrentVersion || storage.Metadata["proxy_url"] != "http://proxy-b.invalid:8080" || storage.Metadata["plugin_data"] != "preserved" {
		t.Fatal("receipt adoption replaced destination credential controls")
	}
}

func TestRefreshTokensStillDeduplicatesMatchingProxyRoutes(t *testing.T) {
	for _, test := range []struct {
		name, selectedA, selectedB, globalB string
	}{
		{name: "same_explicit_proxy", selectedA: "http://proxy.invalid:8080", selectedB: "http://proxy.invalid:8080"},
		{name: "same_inherited_proxy", selectedA: "http://proxy.invalid:8080", globalB: "http://proxy.invalid:8080"},
		{name: "direct_alias", selectedA: "direct", selectedB: "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.ProxyURL = test.globalB
			authA := NewCodexAuthWithProxyURL(nil, test.selectedA)
			authB := NewCodexAuthWithProxyURL(cfg, test.selectedB)
			started := make(chan struct{})
			release := make(chan struct{})
			var startOnce, releaseOnce sync.Once
			var calls atomic.Int32
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				startOnce.Do(func() { close(started) })
				<-release
				return codexProxyRefreshTestResponse(request, "shared-access"), nil
			})
			authA.httpClient.Transport = transport
			authB.httpClient.Transport = transport
			firstDone := make(chan error, 1)
			go func() {
				_, errRefresh := authA.RefreshTokens(context.Background(), t.Name())
				firstDone <- errRefresh
			}()
			<-started
			// DoChan registers the second waiter synchronously, avoiding sleeps or
			// scheduler assumptions while exercising the same production key.
			second := codexRefreshGroup.DoChan(authB.refreshSingleflightKey(t.Name()), func() (any, error) {
				return authB.refreshTokensSingleFlight(context.Background(), t.Name())
			})
			releaseOnce.Do(func() { close(release) })
			if errRefresh := <-firstDone; errRefresh != nil {
				t.Fatal(errRefresh)
			}
			result := <-second
			if result.Err != nil || !result.Shared || calls.Load() != 1 {
				t.Fatalf("same route refresh: shared=%t calls=%d error=%v", result.Shared, calls.Load(), result.Err)
			}
		})
	}
}

func codexProxyRefreshTestResponse(request *http.Request, accessToken string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"access_token":"` + accessToken + `","refresh_token":"new-refresh","expires_in":3600}`)),
		Header:     make(http.Header), Request: request,
	}
}
