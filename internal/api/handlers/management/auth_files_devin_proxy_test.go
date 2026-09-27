package management

import (
	"context"
	"encoding/json"
	"io"
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

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newDevinProxyTestHandler(t *testing.T, global string) *Handler {
	t.Helper()
	cfg := &config.Config{AuthDir: t.TempDir(), Port: 8317, SDKConfig: config.SDKConfig{ProxyURL: global}}
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(cfg.AuthDir)
	return &Handler{cfg: cfg, tokenStore: store, authManager: coreauth.NewManager(store, nil, nil)}
}

func startDevinProxyTest(t *testing.T, h *Handler, method, target, body string) (int, string, string) {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.RequestDevinToken(c)
	var payload struct{ State string }
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	if payload.State != "" {
		t.Cleanup(func() { CancelOAuthSession(payload.State) })
	}
	return response.Code, payload.State, response.Body.String()
}

func TestDevinOAuthProxyRequestValidation(t *testing.T) {
	var constructed atomic.Int32
	originalFactory := newDevinOAuthService
	newDevinOAuthService = func(*config.Config) devinOAuthService {
		constructed.Add(1)
		return &fakeDevinOAuthService{}
	}
	t.Cleanup(func() { newDevinOAuthService = originalFactory })
	for _, test := range []struct {
		name, global, method, target, body string
	}{
		{name: "invalid explicit", method: http.MethodPost, body: `{"proxy_url":"invalid://user:secret@proxy.example"}`},
		{name: "invalid inherited", global: "invalid://user:secret@global.example", method: http.MethodPost, body: `{}`},
		{name: "empty explicit", method: http.MethodPost, body: `{"proxy_url":" "}`},
		{name: "malformed json", method: http.MethodPost, body: `{`},
		{name: "proxy query", method: http.MethodGet, target: "/devin-auth-url?proxy_url=http%3A%2F%2Fuser%3Asecret%40proxy.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := test.target
			if target == "" {
				target = "/devin-auth-url"
			}
			status, state, body := startDevinProxyTest(t, newDevinProxyTestHandler(t, test.global), test.method, target, test.body)
			if status != http.StatusBadRequest || state != "" || strings.Contains(body, "secret") {
				t.Fatalf("invalid proxy was accepted or exposed: status=%d state=%q", status, state)
			}
		})
	}
	if constructed.Load() != 0 {
		t.Fatal("invalid request constructed an OAuth service")
	}
}

// HTTPS is rewritten only in this fixture so the real auth service can exchange
// against an ordinary local HTTP proxy without certificates or external access.
type devinProxyFixtureTransport struct{ base http.RoundTripper }

func (transport devinProxyFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.URL.Scheme = "http"
	return transport.base.RoundTrip(cloned)
}

type devinProxyFixture struct {
	server *httptest.Server
	mu     sync.Mutex
	paths  []string
}

func newDevinProxyFixture(t *testing.T, account string) *devinProxyFixture {
	t.Helper()
	fixture := &devinProxyFixture{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		fixture.paths = append(fixture.paths, request.URL.Path)
		fixture.mu.Unlock()
		switch request.URL.Path {
		case "/auth/cli/token":
			var body map[string]string
			if errDecode := json.NewDecoder(request.Body).Decode(&body); errDecode != nil || body["code"] == "" || body["code_verifier"] == "" {
				t.Error("exchange omitted authorization code or PKCE verifier")
			}
			_, _ = io.WriteString(w, `{"token":"eyJ.`+account+`.token"}`)
		case "/v3/self":
			if request.Header.Get("Authorization") != "Bearer devin-session-token$eyJ."+account+".token" {
				t.Error("profile request did not use this proxy's exchanged token")
			}
			_, _ = io.WriteString(w, `{"user_name":"`+account+`","user_id":"`+account+`-id"}`)
		case devin.DevinGetUserStatusPath:
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), "devin-session-token$eyJ."+account+".token") {
				t.Error("status request did not use this proxy's exchanged token")
			}
			_, _ = w.Write([]byte{0x0a, 0x00})
		default:
			t.Errorf("unexpected proxy request path %q", request.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func installDevinProxyFixtureFactory(t *testing.T) {
	t.Helper()
	originalFactory := newDevinOAuthService
	newDevinOAuthService = func(cfg *config.Config) devinOAuthService {
		client, errClient := devin.NewOAuthHTTPClient(cfg.ProxyURL)
		if errClient != nil {
			t.Errorf("build selected OAuth client: %v", errClient)
			return nil
		}
		client.Transport = devinProxyFixtureTransport{base: client.Transport}
		return devin.NewDevinAuthService(client)
	}
	t.Cleanup(func() { newDevinOAuthService = originalFactory })
}

func TestDevinOAuthSessionsFreezeProxyForExchangeProfileStatusAndSave(t *testing.T) {
	selected := newDevinProxyFixture(t, "selected")
	inherited := newDevinProxyFixture(t, "inherited")
	installDevinProxyFixtureFactory(t)
	h := newDevinProxyTestHandler(t, inherited.server.URL)
	h.cfg.TLS.Enable = true
	authDir := h.cfg.AuthDir
	saved := make(chan *coreauth.Auth, 2)
	h.postAuthPersistHook = func(_ context.Context, record *coreauth.Auth) error {
		saved <- record.Clone()
		return nil
	}
	// The selected session route also survives a plugin hook supplying a different value.
	h.postAuthHook = func(_ context.Context, record *coreauth.Auth) error {
		record.ProxyURL = "http://127.0.0.1:1"
		record.Metadata["proxy_url"] = "http://127.0.0.1:1"
		record.Metadata["plugin_custom_field"] = "preserved"
		return nil
	}
	status, firstState, body := startDevinProxyTest(t, h, http.MethodPost, "/devin-auth-url", `{"proxy_url":"`+selected.server.URL+`","is_webui":true}`)
	if status != http.StatusOK {
		t.Fatalf("selected login: %d %s", status, body)
	}
	redirectForResponse := func(body string) string {
		t.Helper()
		var payload struct{ URL string }
		if errDecode := json.Unmarshal([]byte(body), &payload); errDecode != nil {
			t.Fatal(errDecode)
		}
		authorizationURL, errParse := url.Parse(payload.URL)
		if errParse != nil {
			t.Fatal(errParse)
		}
		redirect := authorizationURL.Query().Get("redirect_uri")
		if redirect != "http://127.0.0.1:8317/callback" {
			t.Fatalf("TLS-enabled login redirect = %q", redirect)
		}
		return redirect
	}
	firstRedirect := redirectForResponse(body)
	status, secondState, body := startDevinProxyTest(t, h, http.MethodGet, "/devin-auth-url?is_webui=true", "")
	if status != http.StatusOK {
		t.Fatalf("legacy inherited login: %d %s", status, body)
	}
	redirectForResponse(body)
	h.mu.Lock()
	h.cfg = h.cfg.CloneForRuntime()
	h.cfg.ProxyURL = "http://127.0.0.1:1"
	h.cfg.AuthDir = t.TempDir()
	h.cfg.Port = 9317
	h.mu.Unlock()
	callbackRouter := gin.New()
	callbackRouter.POST("/oauth-callback", h.PostOAuthCallback)
	for index, state := range []string{firstState, secondState} {
		response := httptest.NewRecorder()
		redirect := firstRedirect
		if index == 1 {
			// Previously issued callbacks remain accepted after the native path change.
			redirect = "http://127.0.0.1:8317/devin/callback"
		}
		callbackBody := `{"provider":"devin","redirect_url":"` + redirect + `?code=test-code&state=` + state + `"}`
		callbackRouter.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/oauth-callback", strings.NewReader(callbackBody)))
		if response.Code != http.StatusOK {
			t.Fatalf("callback after config reload: %d %s", response.Code, response.Body.String())
		}
	}
	seen := make(map[string]bool)
	for range 2 {
		select {
		case record := <-saved:
			seen[record.ProxyURL] = true
			if record.Metadata["proxy_url"] != record.ProxyURL || record.Metadata["plugin_custom_field"] != "preserved" {
				t.Fatal("credential lost frozen proxy or plugin metadata")
			}
			if filepath.Dir(record.Attributes[coreauth.AttributePath]) != authDir {
				t.Fatal("credential saved into reconfigured auth directory")
			}
			raw, errRead := os.ReadFile(record.Attributes[coreauth.AttributePath])
			if errRead != nil {
				t.Fatal(errRead)
			}
			var metadata map[string]any
			if errDecode := json.Unmarshal(raw, &metadata); errDecode != nil || metadata["proxy_url"] != record.ProxyURL {
				t.Fatal("persisted proxy differs from runtime credential")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("OAuth credential was not persisted")
		}
	}
	if !seen[selected.server.URL] || !seen[inherited.server.URL] {
		t.Fatal("concurrent login sessions shared or changed their proxy route")
	}
	for _, fixture := range []*devinProxyFixture{selected, inherited} {
		fixture.mu.Lock()
		paths := append([]string(nil), fixture.paths...)
		fixture.mu.Unlock()
		if strings.Join(paths, ",") != "/auth/cli/token,/v3/self,"+devin.DevinGetUserStatusPath {
			t.Fatalf("proxy did not carry all acquisition phases: %v", paths)
		}
	}
}

func TestDevinOAuthPostAuthCancellationDoesNotPersist(t *testing.T) {
	h := newDevinProxyTestHandler(t, "")
	originalFactory := newDevinOAuthService
	newDevinOAuthService = func(*config.Config) devinOAuthService { return &fakeDevinOAuthService{} }
	t.Cleanup(func() { newDevinOAuthService = originalFactory })
	stateReady := make(chan string, 1)
	called := make(chan struct{})
	h.postAuthHook = func(context.Context, *coreauth.Auth) error {
		CancelOAuthSession(<-stateReady)
		close(called)
		return nil
	}
	status, returnedState, body := startDevinProxyTest(t, h, http.MethodPost, "/devin-auth-url", `{"proxy_url":"direct"}`)
	if status != http.StatusOK {
		t.Fatalf("start: %d %s", status, body)
	}
	state := returnedState
	stateReady <- state
	path, errCallback := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, "devin", state, "code", "")
	if errCallback != nil {
		t.Fatal(errCallback)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("post-auth hook was not called")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, errStat := os.Stat(path); os.IsNotExist(errStat) {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("cancelled login did not release its callback file")
		case <-ticker.C:
		}
	}
	if _, _, exists := GetOAuthSession(state); exists {
		t.Fatal("cancelled OAuth session was recreated")
	}
	entries, errRead := os.ReadDir(h.cfg.AuthDir)
	if errRead != nil || len(entries) != 0 {
		t.Fatalf("cancelled login saved files: %v, %v", entries, errRead)
	}
}
