package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	metaauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type fakeMetaOAuthService struct {
	proceed <-chan struct{}
	account string
}

func (s *fakeMetaOAuthService) StartDeviceFlow(context.Context) (*metaauth.DeviceCodeResponse, error) {
	return &metaauth.DeviceCodeResponse{DeviceCode: "synthetic", UserCode: "LOCAL", VerificationURIComplete: "https://example.test/device", ExpiresIn: 60}, nil
}

func (s *fakeMetaOAuthService) WaitForAuthorization(ctx context.Context, _ *metaauth.DeviceCodeResponse) (*metaauth.MetaAuthBundle, error) {
	if s.proceed != nil {
		select {
		case <-s.proceed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &metaauth.MetaAuthBundle{
		TokenData: &metaauth.TokenData{AccessToken: "dca:" + s.account, TokenType: "Bearer", ExpiresAt: 2000000000},
		MintedKey: &metaauth.MintedKeyResponse{APIKey: "LLM|" + s.account, UserEmail: s.account + "@example.test"},
	}, nil
}

func (s *fakeMetaOAuthService) CreateTokenStorage(bundle *metaauth.MetaAuthBundle) *metaauth.MetaTokenStorage {
	return metaauth.NewMetaAuth(nil).CreateTokenStorage(bundle)
}

func startMetaProxyTest(t *testing.T, h *Handler, method, target, body string) (int, string, string) {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.RequestMetaToken(c)
	var payload struct{ State string }
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	if payload.State != "" {
		t.Cleanup(func() { CancelOAuthSession(payload.State) })
	}
	return response.Code, payload.State, response.Body.String()
}

func TestMetaOAuthProxyRequestValidation(t *testing.T) {
	var constructed atomic.Int32
	original := newMetaOAuthService
	newMetaOAuthService = func(*config.Config) metaOAuthService { constructed.Add(1); return &fakeMetaOAuthService{} }
	t.Cleanup(func() { newMetaOAuthService = original })
	for _, test := range []struct{ name, global, method, target, body string }{
		{name: "invalid explicit", method: http.MethodPost, body: `{"proxy_url":"invalid://user:secret@proxy.example"}`},
		{name: "invalid global", global: "invalid://user:secret@global.example", method: http.MethodPost, body: `{}`},
		{name: "blank explicit", method: http.MethodPost, body: `{"proxy_url":" "}`},
		{name: "bad json", method: http.MethodPost, body: `{`},
		{name: "query proxy", method: http.MethodGet, target: "/meta-auth-url?proxy_url=http%3A%2F%2Fuser%3Asecret%40proxy.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := test.target
			if target == "" {
				target = "/meta-auth-url"
			}
			status, state, body := startMetaProxyTest(t, newDevinProxyTestHandler(t, test.global), test.method, target, test.body)
			if status != http.StatusBadRequest || state != "" || strings.Contains(body, "secret") {
				t.Fatal("invalid proxy request accepted or exposed")
			}
		})
	}
	if constructed.Load() != 0 {
		t.Fatal("invalid request started an auth service")
	}
}

func TestMetaOAuthSessionsFreezeConfigurationProxyAndPersistence(t *testing.T) {
	h := newDevinProxyTestHandler(t, "http://global.example:8118")
	authDir := h.cfg.AuthDir
	proceed := make(chan struct{})
	var routes []string
	original := newMetaOAuthService
	newMetaOAuthService = func(cfg *config.Config) metaOAuthService {
		routes = append(routes, cfg.ProxyURL)
		account := "selected"
		if len(routes) == 2 {
			account = "global"
		}
		return &fakeMetaOAuthService{proceed: proceed, account: account}
	}
	t.Cleanup(func() { newMetaOAuthService = original })
	saved := make(chan *coreauth.Auth, 2)
	h.postAuthHook = func(_ context.Context, record *coreauth.Auth) error {
		record.ProxyURL = "http://wrong.example:1"
		record.Metadata["proxy_url"] = record.ProxyURL
		record.Metadata["plugin_custom_field"] = "preserved"
		return nil
	}
	h.postAuthPersistHook = func(_ context.Context, record *coreauth.Auth) error { saved <- record.Clone(); return nil }
	for _, request := range []struct{ method, body string }{
		{http.MethodPost, `{"proxy_url":"direct","is_webui":true}`},
		{http.MethodGet, ""},
	} {
		status, _, body := startMetaProxyTest(t, h, request.method, "/meta-auth-url", request.body)
		if status != http.StatusOK || !strings.Contains(body, `"flow":"device"`) || !strings.Contains(body, `"user_code":"LOCAL"`) {
			t.Fatalf("device flow start: %d %s", status, body)
		}
	}
	h.mu.Lock()
	h.cfg = h.cfg.CloneForRuntime()
	h.cfg.ProxyURL = "http://changed.example:8118"
	h.cfg.AuthDir = t.TempDir()
	h.mu.Unlock()
	close(proceed)
	seen := map[string]bool{}
	for range 2 {
		select {
		case record := <-saved:
			seen[record.ProxyURL] = true
			if filepath.Dir(record.Attributes[coreauth.AttributePath]) != authDir {
				t.Fatal("auth directory changed during login")
			}
			raw, errRead := os.ReadFile(record.Attributes[coreauth.AttributePath])
			if errRead != nil {
				t.Fatal(errRead)
			}
			var data map[string]any
			if errDecode := json.Unmarshal(raw, &data); errDecode != nil {
				t.Fatal(errDecode)
			}
			if data["proxy_url"] != record.ProxyURL || data["plugin_custom_field"] != "preserved" || data["api_key"] == "" || data["dca_token"] == "" {
				t.Fatal("proxy or native token metadata was lost")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("login did not persist")
		}
	}
	if strings.Join(routes, ",") != "direct,http://global.example:8118" || !seen["direct"] || !seen["http://global.example:8118"] {
		t.Fatal("sessions shared or changed a proxy route")
	}
}

func TestMetaOAuthCancelAfterHookDoesNotPersist(t *testing.T) {
	h := newDevinProxyTestHandler(t, "")
	proceed := make(chan struct{})
	original := newMetaOAuthService
	newMetaOAuthService = func(*config.Config) metaOAuthService {
		return &fakeMetaOAuthService{proceed: proceed, account: "cancelled"}
	}
	t.Cleanup(func() { newMetaOAuthService = original })
	stateReady := make(chan string, 1)
	called := make(chan struct{})
	h.postAuthHook = func(context.Context, *coreauth.Auth) error {
		CancelOAuthSession(<-stateReady)
		close(called)
		return nil
	}
	status, state, body := startMetaProxyTest(t, h, http.MethodPost, "/meta-auth-url", `{"proxy_url":"direct"}`)
	if status != http.StatusOK {
		t.Fatalf("start: %d %s", status, body)
	}
	stateReady <- state
	close(proceed)
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("hook was not called")
	}
	entries, errRead := os.ReadDir(h.cfg.AuthDir)
	if errRead != nil || len(entries) != 0 {
		t.Fatal("cancelled login saved credentials")
	}
	if _, _, exists := GetOAuthSession(state); exists {
		t.Fatal("cancelled session was recreated")
	}
}
