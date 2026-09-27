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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fakeKimiProxyOAuth struct {
	domain  string
	proceed <-chan struct{}
}

func (service *fakeKimiProxyOAuth) StartDeviceFlow(context.Context) (*kimi.DeviceCodeResponse, error) {
	return &kimi.DeviceCodeResponse{DeviceCode: "fixture", UserCode: "fixture-user", VerificationURI: "https://kimi.invalid/authorize", ExpiresIn: 60}, nil
}
func (service *fakeKimiProxyOAuth) WaitForAuthorization(ctx context.Context, _ *kimi.DeviceCodeResponse) (*kimi.KimiAuthBundle, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-service.proceed:
	}
	return &kimi.KimiAuthBundle{DeviceID: "fixture-device", TokenData: &kimi.KimiTokenData{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}}, nil
}
func (service *fakeKimiProxyOAuth) CreateTokenStorage(bundle *kimi.KimiAuthBundle) *kimi.KimiTokenStorage {
	return kimi.NewKimiAuthWithDomain(nil, service.domain).CreateTokenStorage(bundle)
}

func startKimiProxyTest(t *testing.T, h *Handler, ai bool, method, target, body string) (int, string, map[string]any) {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if ai {
		h.RequestKimiAIToken(c)
	} else {
		h.RequestKimiToken(c)
	}
	var payload map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	state, _ := payload["state"].(string)
	if state != "" {
		t.Cleanup(func() { CancelOAuthSession(state) })
	}
	return response.Code, state, payload
}

func TestKimiOAuthProxyValidationBeforeAcquisition(t *testing.T) {
	var constructed atomic.Int32
	original := newKimiOAuthService
	newKimiOAuthService = func(*config.Config, string) kimiOAuthService { constructed.Add(1); return nil }
	t.Cleanup(func() { newKimiOAuthService = original })
	for _, ai := range []bool{false, true} {
		for _, test := range []struct{ name, global, method, target, body string }{
			{name: "invalid explicit", method: http.MethodPost, body: `{"proxy_url":"invalid://user:secret@proxy.invalid"}`},
			{name: "invalid global", global: "invalid://user:secret@proxy.invalid", method: http.MethodPost, body: `{}`},
			{name: "empty explicit", method: http.MethodPost, body: `{"proxy_url":" "}`},
			{name: "malformed JSON", method: http.MethodPost, body: `{`},
			{name: "query leak", method: http.MethodGet, target: "/kimi-auth-url?proxy_url=http%3A%2F%2Fsecret%40proxy.invalid"},
		} {
			t.Run(test.name, func(t *testing.T) {
				target := test.target
				if target == "" {
					target = "/kimi-auth-url"
				}
				status, state, payload := startKimiProxyTest(t, newDevinProxyTestHandler(t, test.global), ai, test.method, target, test.body)
				raw, _ := json.Marshal(payload)
				if status != http.StatusBadRequest || state != "" || strings.Contains(string(raw), "secret") {
					t.Fatalf("invalid OAuth proxy accepted or exposed: %d %s", status, raw)
				}
			})
		}
	}
	if constructed.Load() != 0 {
		t.Fatal("invalid request started acquisition")
	}
}

func TestKimiOAuthFreezesProxyDomainAndAuthDirectory(t *testing.T) {
	for _, test := range []struct {
		name, method, body, expectedProxy, domain, provider string
		ai                                                  bool
	}{
		{name: "AI explicit", ai: true, method: http.MethodPost, body: `{"proxy_url":"http://selected.example:8118"}`, expectedProxy: "http://selected.example:8118", domain: "kimi.ai", provider: "kimi-ai"},
		{name: "AI direct", ai: true, method: http.MethodPost, body: `{"proxy_url":"direct"}`, expectedProxy: "direct", domain: "kimi.ai", provider: "kimi-ai"},
		{name: "Com legacy GET", method: http.MethodGet, expectedProxy: "http://global.example:8118", domain: "kimi.com", provider: "kimi"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newDevinProxyTestHandler(t, "http://global.example:8118")
			originalDir := h.cfg.AuthDir
			proceed := make(chan struct{})
			original := newKimiOAuthService
			newKimiOAuthService = func(cfg *config.Config, domain string) kimiOAuthService {
				if cfg.ProxyURL != test.expectedProxy || domain != test.domain {
					t.Fatalf("incorrect acquisition snapshot proxy=%q domain=%q", cfg.ProxyURL, domain)
				}
				return &fakeKimiProxyOAuth{domain: domain, proceed: proceed}
			}
			t.Cleanup(func() { newKimiOAuthService = original })
			saved := make(chan *coreauth.Auth, 1)
			h.postAuthPersistHook = func(_ context.Context, record *coreauth.Auth) error { saved <- record.Clone(); return nil }
			h.postAuthHook = func(_ context.Context, record *coreauth.Auth) error {
				record.ProxyURL = "http://hook.invalid"
				record.Metadata["proxy_url"] = record.ProxyURL
				record.Metadata["concurrency_limit"] = 3
				return nil
			}
			status, _, payload := startKimiProxyTest(t, h, test.ai, test.method, "/kimi-auth-url", test.body)
			if status != http.StatusOK || payload["flow"] != "device" {
				t.Fatalf("start failed: %d %v", status, payload)
			}
			h.mu.Lock()
			h.cfg.ProxyURL = "http://changed.example:8118"
			h.cfg.AuthDir = t.TempDir()
			h.mu.Unlock()
			close(proceed)
			select {
			case record := <-saved:
				if record.Provider != test.provider || record.ProxyURL != test.expectedProxy {
					t.Fatalf("saved wrong route/provider: %q %q", record.ProxyURL, record.Provider)
				}
				data, errRead := os.ReadFile(filepath.Join(originalDir, record.FileName))
				if errRead != nil {
					t.Fatal(errRead)
				}
				var disk map[string]any
				if errDecode := json.Unmarshal(data, &disk); errDecode != nil {
					t.Fatal(errDecode)
				}
				if disk["type"] != test.provider || disk["domain"] != test.domain || disk["proxy_url"] != test.expectedProxy || disk["concurrency_limit"] != float64(3) || disk["access_token"] != "fixture-access" {
					t.Fatalf("saved controls or credentials lost: %v", disk)
				}
				entries, errRead := os.ReadDir(h.cfg.AuthDir)
				if errRead != nil || len(entries) != 0 {
					t.Fatal("login moved to mutated auth directory")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("login did not persist")
			}
		})
	}
}

func TestKimiOAuthCancellationInsideHookPreventsSave(t *testing.T) {
	h := newDevinProxyTestHandler(t, "")
	proceed := make(chan struct{})
	original := newKimiOAuthService
	newKimiOAuthService = func(*config.Config, string) kimiOAuthService {
		return &fakeKimiProxyOAuth{domain: "kimi.ai", proceed: proceed}
	}
	t.Cleanup(func() { newKimiOAuthService = original })
	stateReady := make(chan string, 1)
	called := make(chan struct{})
	h.postAuthHook = func(context.Context, *coreauth.Auth) error {
		CancelOAuthSession(<-stateReady)
		close(called)
		return nil
	}
	status, state, _ := startKimiProxyTest(t, h, true, http.MethodPost, "/kimi-ai-auth-url", `{"proxy_url":"direct"}`)
	if status != http.StatusOK {
		t.Fatalf("start status %d", status)
	}
	stateReady <- state
	close(proceed)
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not run")
	}
	entries, errRead := os.ReadDir(h.cfg.AuthDir)
	if errRead != nil || len(entries) != 0 {
		t.Fatal("cancelled login saved credentials")
	}
}
