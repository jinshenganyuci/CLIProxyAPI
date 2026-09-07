package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func newCodexOAuthProxyTestHandler(t *testing.T, global, policy string) *Handler {
	t.Helper()
	cfg := &config.Config{AuthDir: t.TempDir(), SDKConfig: config.SDKConfig{ProxyURL: global}, Codex: config.CodexConfig{CredentialProxyPolicy: policy}}
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(cfg.AuthDir)
	return &Handler{cfg: cfg, tokenStore: store, authManager: coreauth.NewManager(store, nil, nil)}
}
func prepareCodexOAuthProxyTest(t *testing.T, h *Handler, method, target, body string) (*codexOAuthSessionOptions, int, error) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return h.prepareCodexOAuthSession(c)
}
func TestCodexOAuthProxySelection(t *testing.T) {
	for _, tc := range []struct {
		name, global, policy, body, expected string
		status                               int
	}{
		{"explicit", "http://global.example:8080", "prefer", `{"proxy_url":"socks5://proxy.example:1080"}`, "socks5://proxy.example:1080", 200},
		{"global_snapshot", "http://global.example:8080", "prefer", `{}`, "http://global.example:8080", 200},
		{"empty_global_direct", "", "prefer", `{}`, "direct", 200},
		{"explicit_direct", "http://global.example:8080", "require", `{"proxy_url":"direct"}`, "direct", 200},
		{"require_explicit", "", "require", `{"proxy_url":"http://proxy.example:8080"}`, "http://proxy.example:8080", 200},
		{"require_rejects_global", "http://global.example:8080", "require", `{}`, "", 409},
		{"require_rejects_empty", "", "require", `{}`, "", 409},
		{"empty_explicit", "http://global.example:8080", "prefer", `{"proxy_url":""}`, "", 400},
		{"invalid_explicit", "http://global.example:8080", "prefer", `{"proxy_url":"invalid://user:secret@proxy.example"}`, "", 400},
		{"invalid_global", "invalid://proxy.example", "prefer", `{}`, "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCodexOAuthProxyTestHandler(t, tc.global, tc.policy)
			options, status, err := prepareCodexOAuthProxyTest(t, h, http.MethodPost, "/codex-auth-url", tc.body)
			if status != tc.status {
				t.Fatalf("status=%d want=%d", status, tc.status)
			}
			if tc.status != 200 {
				if err == nil || strings.Contains(err.Error(), "secret") {
					t.Fatal("expected sanitized proxy selection error")
				}
				return
			}
			if err != nil || options.proxyURL != tc.expected || options.handler.cfg.ProxyURL != tc.expected {
				t.Fatal("session did not bind expected effective proxy")
			}
			if h.cfg.ProxyURL != tc.global {
				t.Fatal("login changed global proxy config")
			}
		})
	}
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	_, status, err := prepareCodexOAuthProxyTest(t, h, http.MethodGet, "/codex-auth-url?proxy_url=http%3A%2F%2Fuser%3Asecret%40proxy.example", "")
	if status != 400 || err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("proxy credentials in URL were not rejected safely")
	}
	options, status, err := prepareCodexOAuthProxyTest(t, h, http.MethodGet, "/codex-auth-url?is_webui=true", "")
	if status != 200 || err != nil || !options.isWebUI || options.proxyURL != "direct" {
		t.Fatal("legacy GET behavior changed")
	}
}

type codexOAuthProxyTransport struct {
	proxy     string
	exchanged chan<- string
}

func (transport codexOAuthProxyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.exchanged <- transport.proxy
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

type codexOAuthProxyTestService struct {
	fakeCodexOAuthService
	cfg       *config.Config
	exchanged chan<- string
}

func (service *codexOAuthProxyTestService) ExchangeCodeForTokens(ctx context.Context, code string, pkce *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	client := &http.Client{Transport: codexOAuthProxyTransport{proxy: service.cfg.ProxyURL, exchanged: service.exchanged}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth.example.test/token", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if errClose := response.Body.Close(); errClose != nil {
		return nil, errClose
	}
	return service.fakeCodexOAuthService.ExchangeCodeForTokens(ctx, code, pkce)
}
func TestCodexOAuthSessionsKeepIndependentProxySnapshots(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "http://global.example:8080", "prefer")
	authDir := h.cfg.AuthDir
	exchanged := make(chan string, 2)
	saved := make(chan *coreauth.Auth, 2)
	h.postAuthPersistHook = func(_ context.Context, auth *coreauth.Auth) error { saved <- auth.Clone(); return nil }
	originalFactory := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		return &codexOAuthProxyTestService{cfg: cfg, exchanged: exchanged}
	}
	t.Cleanup(func() { newCodexOAuthService = originalFactory })
	states := make([]string, 0, 2)
	for _, body := range []string{`{"proxy_url":"socks5://user:secret@selected.example:1080"}`, `{}`} {
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodPost, "/codex-auth-url", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.RequestCodexToken(c)
		if response.Code != 200 || strings.Contains(response.Body.String(), "secret") {
			t.Fatal("OAuth creation failed or exposed proxy credentials")
		}
		var payload struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.State == "" {
			t.Fatal("OAuth state missing")
		}
		states = append(states, payload.State)
		t.Cleanup(func() { CancelOAuthSession(payload.State) })
	}
	h.mu.Lock()
	h.cfg.ProxyURL = "http://changed.example:8081"
	h.cfg.AuthDir = t.TempDir()
	h.mu.Unlock()
	for index, state := range states {
		code := "first"
		if index == 1 {
			code = "second"
		}
		if _, err := WriteOAuthCallbackFileForPendingSession(authDir, "codex", state, code, ""); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]bool)
	for range states {
		select {
		case proxy := <-exchanged:
			seen[proxy] = true
		case <-time.After(5 * time.Second):
			t.Fatal("synthetic token exchange timed out")
		}
	}
	if !seen["socks5://user:secret@selected.example:1080"] || !seen["http://global.example:8080"] {
		t.Fatal("concurrent OAuth exchanges reused a session or changed global proxy")
	}
	for range states {
		select {
		case auth := <-saved:
			if !seen[auth.ProxyURL] || auth.Metadata["proxy_url"] != auth.ProxyURL {
				t.Fatal("saved auth lost session proxy")
			}
			if filepath.Dir(auth.Attributes[coreauth.AttributePath]) != authDir {
				t.Fatal("OAuth saved into reconfigured auth directory")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("synthetic credential persistence timed out")
		}
	}
}

func registerCodexOAuthTarget(t *testing.T, h *Handler) *coreauth.Auth {
	t.Helper()
	metadata := map[string]any{"type": "codex", "account_id": "selected-account", "access_token": "old-access", "proxy_url": "socks5://old.example:1080"}
	if err := codex.SetCredentialIdentity(metadata, codex.NewCredentialIdentityNamespace()); err != nil {
		t.Fatal(err)
	}
	auth, err := h.authManager.Register(context.Background(), &coreauth.Auth{ID: "selected-original.json", FileName: "selected-original.json", Provider: "codex", ProxyURL: "socks5://old.example:1080", Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	return auth
}
func TestCodexOAuthReauthenticationKeepsAccountFileIdentityAndProxy(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "http://global.example:8080", "require")
	target := registerCodexOAuthTarget(t, h)
	options, status, err := prepareCodexOAuthProxyTest(t, h, http.MethodPost, "/codex-auth-url", `{"auth_index":"`+lockedAuthIndex(target)+`"}`)
	if err != nil || status != 200 || options.proxyURL != target.ProxyURL {
		t.Fatal("reauthentication did not select old proxy before exchange")
	}
	bundle := &codex.CodexAuthBundle{TokenData: codex.CodexTokenData{AccountID: "wrong-account", Email: "new-name@example.test", AccessToken: "new-access"}}
	service := &fakeCodexOAuthService{}
	if _, errRecord := options.record(service, bundle); errRecord == nil {
		t.Fatal("reauthentication accepted different account")
	}
	bundle.TokenData.AccountID = "selected-account"
	record, errRecord := options.record(service, bundle)
	if errRecord != nil || record.ID != target.ID || record.FileName != target.FileName || !codexIdentityFieldsEqual(record.Metadata, target.Metadata) {
		t.Fatal("reauthentication lost selected file or namespace")
	}
	state := codex.NewCredentialIdentityNamespace()
	RegisterOAuthSession(state, "codex")
	t.Cleanup(func() { CancelOAuthSession(state) })
	if _, errSave := options.save(context.Background(), state, record); errSave != nil {
		t.Fatal(errSave)
	}
	persisted := readCodexIdentityTestFile(t, filepath.Join(h.cfg.AuthDir, target.FileName))
	if persisted["access_token"] != "new-access" || persisted["proxy_url"] != target.ProxyURL || !codexIdentityFieldsEqual(persisted, target.Metadata) {
		t.Fatal("reauthentication did not save token with selected route and identity")
	}
}
func TestCodexOAuthExplicitProxyOverridesOldMetadataAndHooks(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "require")
	target := registerCodexOAuthTarget(t, h)
	h.postAuthHook = func(_ context.Context, auth *coreauth.Auth) error {
		auth.ProxyURL = "http://hook.example:8080"
		auth.Metadata["proxy_url"] = auth.ProxyURL
		return nil
	}
	options, status, err := prepareCodexOAuthProxyTest(t, h, http.MethodPost, "/codex-auth-url", `{"auth_index":"`+lockedAuthIndex(target)+`","proxy_url":"direct"}`)
	if status != 200 || err != nil {
		t.Fatal("explicit reauthentication proxy selection failed")
	}
	record, errRecord := options.record(&fakeCodexOAuthService{}, &codex.CodexAuthBundle{TokenData: codex.CodexTokenData{AccountID: "selected-account", AccessToken: "new-access"}})
	if errRecord != nil {
		t.Fatal(errRecord)
	}
	state := codex.NewCredentialIdentityNamespace()
	RegisterOAuthSession(state, "codex")
	t.Cleanup(func() { CancelOAuthSession(state) })
	if _, errSave := options.save(context.Background(), state, record); errSave != nil {
		t.Fatal(errSave)
	}
	if record.ProxyURL != "direct" || readCodexIdentityTestFile(t, options.targetPath)["proxy_url"] != "direct" {
		t.Fatal("old metadata or post-auth hook replaced session proxy")
	}
	CancelOAuthSession(state)
	if _, errSave := options.save(context.Background(), state, record); errSave == nil {
		t.Fatal("cancelled session saved credentials")
	}
}

func TestCodexOAuthCancellationDuringPostAuthHookDoesNotSave(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	state := codex.NewCredentialIdentityNamespace()
	h.postAuthHook = func(context.Context, *coreauth.Auth) error { CancelOAuthSession(state); return nil }
	options, status, errPrepare := prepareCodexOAuthProxyTest(t, h, http.MethodPost, "/codex-auth-url", `{"proxy_url":"direct"}`)
	if errPrepare != nil || status != 200 {
		t.Fatal("failed to prepare synthetic OAuth")
	}
	record, errRecord := options.record(&fakeCodexOAuthService{}, &codex.CodexAuthBundle{TokenData: codex.CodexTokenData{AccountID: "synthetic", Email: "synthetic@example.test", AccessToken: "synthetic-token"}})
	if errRecord != nil {
		t.Fatal(errRecord)
	}
	RegisterOAuthSession(state, "codex")
	t.Cleanup(func() { CancelOAuthSession(state) })
	if _, errSave := options.save(context.Background(), state, record); errSave == nil {
		t.Fatal("cancel during post-auth hook still saved credentials")
	}
	if _, errStat := os.Stat(filepath.Join(h.cfg.AuthDir, record.FileName)); !os.IsNotExist(errStat) {
		t.Fatal("cancelled OAuth created an auth file")
	}
}
