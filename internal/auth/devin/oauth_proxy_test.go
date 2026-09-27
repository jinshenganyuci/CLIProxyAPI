package devin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/credentialfile"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDevinOAuthProxyResolve(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"empty freezes direct", "", "direct"},
		{"whitespace freezes direct", " \t", "direct"},
		{"direct", " DiReCt ", "direct"},
		{"none", " NONE ", "direct"},
		{"http", " http://user:password@127.0.0.1:8080 ", "http://user:password@127.0.0.1:8080"},
		{"https", "https://127.0.0.1:8443", "https://127.0.0.1:8443"},
		{"socks5", "socks5://127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{"socks5h", "socks5h://127.0.0.1:1080", "socks5h://127.0.0.1:1080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, errResolve := ResolveOAuthProxy(tc.raw)
			if errResolve != nil || got != tc.want {
				t.Fatalf("resolved route = %q, error = %v; want %q", got, errResolve, tc.want)
			}
		})
	}
}

func TestDevinOAuthProxyRejectsInvalidSelectionWithoutSecret(t *testing.T) {
	for _, raw := range []string{
		"ftp://private-user:private-password@proxy.invalid/private-path",
		"http://private-user:private-password@",
		"http://private-user:private-password@proxy.invalid:bad-port/private-path",
		"private-user:private-password",
	} {
		t.Run(strings.SplitN(raw, ":", 2)[0], func(t *testing.T) {
			resolved, errResolve := ResolveOAuthProxy(raw)
			client, errClient := NewOAuthHTTPClient(raw)
			record := &coreauth.Auth{ProxyURL: "direct", Metadata: map[string]any{"marker": "preserved"}}
			errBind := BindOAuthProxy(record, raw, true)
			if resolved != "" || client != nil {
				t.Fatal("invalid proxy produced a usable route or client")
			}
			for _, errProxy := range []error{errResolve, errClient, errBind} {
				if errProxy == nil {
					t.Fatal("invalid proxy was accepted")
				}
				for _, secret := range []string{"private-user", "private-password", "private-path", "proxy.invalid"} {
					if strings.Contains(errProxy.Error(), secret) {
						t.Fatal("invalid proxy error disclosed selection details")
					}
				}
			}
			if record.ProxyURL != "direct" || record.Storage != nil || len(record.Metadata) != 1 || record.Metadata["marker"] != "preserved" {
				t.Fatal("failed binding changed the authentication record")
			}
		})
	}
	if errBind := BindOAuthProxy(nil, "direct", true); errBind == nil {
		t.Fatal("nil authentication record accepted")
	}
}

func TestDevinOAuthProxyDirectBypassesEnvironmentAndDefaultTransport(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	proxyURL, errParse := url.Parse(proxy.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	previousDefault := http.DefaultTransport
	defaultTransport := previousDefault.(*http.Transport).Clone()
	defaultTransport.Proxy = http.ProxyURL(proxyURL)
	http.DefaultTransport = defaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = previousDefault
		defaultTransport.CloseIdleConnections()
	})
	var originCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originCalls.Add(1)
		_, _ = w.Write([]byte("direct-origin"))
	}))
	defer origin.Close()
	for _, raw := range []string{"direct", "none", ""} {
		client, errClient := NewOAuthHTTPClient(raw)
		if errClient != nil {
			t.Fatal(errClient)
		}
		transport := client.Transport.(*http.Transport)
		defer transport.CloseIdleConnections()
		if transport.Proxy != nil {
			t.Fatal("direct transport retained a proxy selector")
		}
		response, errRequest := client.Get(origin.URL)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		body, errRead := io.ReadAll(response.Body)
		errClose := response.Body.Close()
		if errRead != nil || errClose != nil || string(body) != "direct-origin" {
			t.Fatalf("unexpected direct response: %q, read error %v, close error %v", body, errRead, errClose)
		}
	}
	if originCalls.Load() != 3 || proxyCalls.Load() != 0 {
		t.Fatalf("origin calls = %d, unwanted proxy calls = %d", originCalls.Load(), proxyCalls.Load())
	}
}

func TestDevinOAuthProxySelectedHTTPProxyReceivesRequest(t *testing.T) {
	var fallbackCalls atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer fallback.Close()
	t.Setenv("HTTP_PROXY", fallback.URL)
	t.Setenv("HTTPS_PROXY", fallback.URL)
	t.Setenv("ALL_PROXY", fallback.URL)
	t.Setenv("NO_PROXY", "")
	requests := make(chan string, 1)
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.String()
		_, _ = w.Write([]byte("selected-route"))
	}))
	defer selected.Close()
	client, errClient := NewOAuthHTTPClient(selected.URL)
	if errClient != nil {
		t.Fatal(errClient)
	}
	defer client.Transport.(*http.Transport).CloseIdleConnections()
	const target = "http://devin-oauth.invalid/token"
	response, errRequest := client.Get(target)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	body, errRead := io.ReadAll(response.Body)
	errClose := response.Body.Close()
	if errRead != nil || errClose != nil || string(body) != "selected-route" {
		t.Fatalf("unexpected selected-proxy response: %q, read error %v, close error %v", body, errRead, errClose)
	}
	if got := <-requests; got != target {
		t.Fatalf("proxy received %q, want %q", got, target)
	}
	if fallbackCalls.Load() != 0 {
		t.Fatal("selected proxy request reached an environment proxy")
	}
}

func TestDevinOAuthTokenBindingPersistsCompleteMetadata(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "inherited", true: "explicit"}[explicit], func(t *testing.T) {
			const route = "http://127.0.0.1:10080"
			path := filepath.Join(t.TempDir(), "nested", "devin-test.json")
			record := &coreauth.Auth{Metadata: map[string]any{"session_token": "fake-initial-token"}}
			if errBind := BindOAuthProxy(record, route, explicit); errBind != nil {
				t.Fatal(errBind)
			}
			if record.ProxyURL != route || record.Metadata["proxy_url"] != route {
				t.Fatal("binding did not attach the acquisition route to both record fields")
			}
			storage, ok := record.Storage.(*oauthTokenStorage)
			if !ok {
				t.Fatal("binding did not install protected OAuth token storage")
			}
			if errSave := storage.SaveTokenToFile(path); errSave != nil {
				t.Fatal(errSave)
			}
			metadata := map[string]any{
				"session_token": "fake-new-token",
				"api_key":       "fake-api-token",
				"type":          "devin",
				"proxy_url":     "http://discarded-metadata-route.invalid:1080",
				"custom":        map[string]any{"retained": true, "labels": []any{"one", "two"}},
			}
			storage.SetMetadata(metadata)
			want := map[string]any{
				"session_token": "fake-new-token",
				"api_key":       "fake-api-token",
				"type":          "devin",
				"proxy_url":     route,
				"custom":        map[string]any{"retained": true, "labels": []any{"one", "two"}},
			}
			if errSave := storage.SaveTokenToFile(path); errSave != nil {
				t.Fatal(errSave)
			}
			assertDevinOAuthTokenFile(t, path, want)
		})
	}
	noMetadata := &coreauth.Auth{}
	if errBind := BindOAuthProxy(noMetadata, "", false); errBind != nil {
		t.Fatal(errBind)
	}
	if noMetadata.ProxyURL != "direct" || noMetadata.Metadata["proxy_url"] != "direct" {
		t.Fatal("empty selection did not freeze an explicit direct route")
	}
}

func TestDevinOAuthTokenImplicitProxyMismatchPreservesFile(t *testing.T) {
	for _, previous := range []string{"http://127.0.0.1:10081", "ftp://private-user:private-password@proxy.invalid/private-path"} {
		t.Run(strings.SplitN(previous, ":", 2)[0], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "devin-test.json")
			original := []byte("{\n  \"session_token\": \"old-token\", \"proxy_url\": " + strconv.Quote(previous) + "\n}\n")
			if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			storage := &oauthTokenStorage{metadata: map[string]any{"session_token": "new-token"}, proxyURL: "direct"}
			if errSave := storage.SaveTokenToFile(path); !errors.Is(errSave, ErrOAuthProxyMismatch) {
				t.Fatalf("expected proxy mismatch, got %v", errSave)
			}
			assertDevinOAuthTokenBytes(t, path, original)
		})
	}
}

func TestDevinOAuthTokenExplicitSelectionReplacesProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devin-test.json")
	if errWrite := os.WriteFile(path, []byte(`{"session_token":"old-token","proxy_url":"http://127.0.0.1:10081"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	metadata := map[string]any{"session_token": "new-token", "retained": "new-metadata"}
	record := &coreauth.Auth{Metadata: metadata}
	if errBind := BindOAuthProxy(record, "direct", true); errBind != nil {
		t.Fatal(errBind)
	}
	if errSave := record.Storage.SaveTokenToFile(path); errSave != nil {
		t.Fatal(errSave)
	}
	assertDevinOAuthTokenFile(t, path, map[string]any{"session_token": "new-token", "retained": "new-metadata", "proxy_url": "direct"})
}

func TestDevinOAuthTokenReadsLatestProxyUnderCredentialLock(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "implicit rejects changed proxy", true: "explicit replaces changed proxy"}[explicit], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "devin-test.json")
			if errWrite := os.WriteFile(path, []byte(`{"session_token":"old-token","proxy_url":"direct"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			metadata := map[string]any{"session_token": "new-token", "retained": "new-metadata"}
			record := &coreauth.Auth{Metadata: metadata}
			if errBind := BindOAuthProxy(record, "direct", explicit); errBind != nil {
				t.Fatal(errBind)
			}
			unlock := credentialfile.Lock(path)
			locked := true
			started := make(chan struct{})
			completed := make(chan error, 1)
			joined := false
			defer func() {
				if locked {
					unlock()
				}
				if !joined {
					<-completed
				}
			}()
			go func() {
				close(started)
				completed <- record.Storage.SaveTokenToFile(path)
			}()
			<-started
			latest := []byte("{\n  \"session_token\": \"concurrent-token\", \"proxy_url\": \"http://127.0.0.1:10082\"\n}\n")
			if errWrite := os.WriteFile(path, latest, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			unlock()
			locked = false
			errSave := <-completed
			joined = true
			if !explicit {
				if !errors.Is(errSave, ErrOAuthProxyMismatch) {
					t.Fatalf("implicit login did not reject the latest proxy: %v", errSave)
				}
				assertDevinOAuthTokenBytes(t, path, latest)
				return
			}
			if errSave != nil {
				t.Fatal(errSave)
			}
			assertDevinOAuthTokenFile(t, path, map[string]any{"session_token": "new-token", "retained": "new-metadata", "proxy_url": "direct"})
		})
	}
}

func TestDevinOAuthTokenInvalidMetadataPreservesFile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		original []byte
		metadata map[string]any
	}{
		{"malformed existing file", []byte("private-existing-invalid-json"), map[string]any{"session_token": "new-token"}},
		{"missing metadata", []byte(`{"proxy_url":"direct","session_token":"old-token"}`), nil},
		{"unserializable metadata", []byte(`{"proxy_url":"direct","session_token":"old-token"}`), map[string]any{"unsupported": make(chan int)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "devin-test.json")
			if errWrite := os.WriteFile(path, tc.original, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			storage := &oauthTokenStorage{metadata: tc.metadata, proxyURL: "direct"}
			errSave := storage.SaveTokenToFile(path)
			if errSave == nil {
				t.Fatal("invalid metadata was accepted")
			}
			if strings.Contains(errSave.Error(), "private-existing-invalid-json") {
				t.Fatal("storage error disclosed existing file contents")
			}
			assertDevinOAuthTokenBytes(t, path, tc.original)
		})
	}
}

func TestDevinOAuthTokenMergesLatestControlsAndActualHookEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devin-test.json")
	existing := map[string]any{
		"type": "devin", "api_key": "old-token", "user_id": "old-profile",
		"proxy_url": "http://127.0.0.1:10081", "disabled": false, "weight": float64(1),
		"plugin": map[string]any{"nested": map[string]any{"enabled": false, "level": float64(1)}},
		"label":  "old-label", "auth_index": "old-index", "deleted_canonically": "old-value",
		"hook_edit": map[string]any{"nested": "before-hook"}, "hook_delete": "before-hook",
	}
	original, errMarshal := json.Marshal(existing)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	record := &coreauth.Auth{Metadata: map[string]any{
		"type": "devin", "api_key": "fresh-token", "user_id": "fresh-profile",
	}}
	const selectedRoute = "http://127.0.0.1:10082"
	if errBind := BindOAuthProxy(record, selectedRoute, true); errBind != nil {
		t.Fatal(errBind)
	}
	coreauth.MergeExistingAuthMetadata(record, existing)
	storage := record.Storage.(*oauthTokenStorage)
	if storage.metadataSets != 1 {
		t.Fatal("generic metadata merge did not establish the first metadata snapshot")
	}
	record.Metadata["hook_added"] = map[string]any{"enabled": true}
	record.Metadata["hook_edit"].(map[string]any)["nested"] = "after-hook"
	delete(record.Metadata, "hook_delete")
	storage.SetMetadata(record.Metadata)
	latest := map[string]any{
		"type": "devin", "api_key": "concurrent-old-token", "user_id": "concurrent-old-profile",
		"proxy_url": "http://127.0.0.1:10083", "disabled": true, "weight": float64(7),
		"plugin": map[string]any{"nested": map[string]any{"enabled": true, "level": float64(9)}},
		"label":  "latest-label", "auth_index": "latest-index", "late_added": "latest-value",
		"hook_edit": map[string]any{"nested": "before-hook"}, "hook_delete": "before-hook",
	}
	latestBytes, errLatest := json.Marshal(latest)
	if errLatest != nil {
		t.Fatal(errLatest)
	}
	unlock := credentialfile.Lock(path)
	locked := true
	started := make(chan struct{})
	completed := make(chan error, 1)
	joined := false
	defer func() {
		if locked {
			unlock()
		}
		if !joined {
			<-completed
		}
	}()
	go func() {
		close(started)
		completed <- storage.SaveTokenToFile(path)
	}()
	<-started
	if errWrite := os.WriteFile(path, latestBytes, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	unlock()
	locked = false
	errSave := <-completed
	joined = true
	if errSave != nil {
		t.Fatal(errSave)
	}
	want := map[string]any{
		"type": "devin", "api_key": "fresh-token", "user_id": "fresh-profile",
		"proxy_url": selectedRoute, "disabled": true, "weight": float64(7),
		"plugin": map[string]any{"nested": map[string]any{"enabled": true, "level": float64(9)}},
		"label":  "latest-label", "auth_index": "latest-index", "late_added": "latest-value",
		"hook_added": map[string]any{"enabled": true}, "hook_edit": map[string]any{"nested": "after-hook"},
	}
	assertDevinOAuthTokenFile(t, path, want)
	if !reflect.DeepEqual(record.Metadata, want) {
		t.Fatal("saved authentication metadata does not match the canonical file")
	}
}

func TestDevinOAuthTokenCancellationWhileWaitingForLockPreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devin-test.json")
	original := []byte("{\n  \"api_key\": \"old-token\", \"proxy_url\": \"direct\"\n}\n")
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	record := &coreauth.Auth{Metadata: map[string]any{"type": "devin", "api_key": "fresh-token"}}
	if errBind := BindOAuthProxy(record, "direct", true, ctx.Err); errBind != nil {
		t.Fatal(errBind)
	}
	unlock := credentialfile.Lock(path)
	locked := true
	started := make(chan struct{})
	completed := make(chan error, 1)
	joined := false
	defer func() {
		if locked {
			unlock()
		}
		if !joined {
			<-completed
		}
	}()
	go func() {
		close(started)
		completed <- record.Storage.SaveTokenToFile(path)
	}()
	<-started
	cancel()
	unlock()
	locked = false
	errSave := <-completed
	joined = true
	if !errors.Is(errSave, context.Canceled) {
		t.Fatalf("canceled login returned %v, want context.Canceled", errSave)
	}
	assertDevinOAuthTokenBytes(t, path, original)
}

func TestDevinOAuthTokenGuardBeforeRenamePreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devin-test.json")
	original := []byte("{\n  \"api_key\": \"old-token\", \"proxy_url\": \"direct\"\n}\n")
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	errInvalidated := errors.New("login session invalidated")
	guardCalls := 0
	record := &coreauth.Auth{Metadata: map[string]any{"type": "devin", "api_key": "fresh-token"}}
	if errBind := BindOAuthProxy(record, "direct", true, func() error {
		guardCalls++
		if guardCalls == 2 {
			return errInvalidated
		}
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}
	if errSave := record.Storage.SaveTokenToFile(path); !errors.Is(errSave, errInvalidated) {
		t.Fatalf("second guard returned %v, want invalidation error", errSave)
	}
	if guardCalls != 2 {
		t.Fatalf("save guard called %d times, want 2", guardCalls)
	}
	assertDevinOAuthTokenBytes(t, path, original)
}

func assertDevinOAuthTokenFile(t *testing.T, path string, want map[string]any) {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var got map[string]any
	if errDecode := json.Unmarshal(data, &got); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted metadata = %#v, want %#v", got, want)
	}
	assertDevinOAuthTokenBytes(t, path, data)
}

func assertDevinOAuthTokenBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("credential bytes changed unexpectedly")
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatal(errStat)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credential permissions = %04o, want 0600", info.Mode().Perm())
	}
	temporary, errGlob := filepath.Glob(filepath.Join(filepath.Dir(path), ".devin-token-*.tmp"))
	if errGlob != nil || len(temporary) != 0 {
		t.Fatalf("temporary credential artifacts remain: %v, error %v", temporary, errGlob)
	}
}
