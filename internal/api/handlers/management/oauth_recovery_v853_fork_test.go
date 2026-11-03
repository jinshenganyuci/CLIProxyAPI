package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV853TargetedOAuthUsesCredentialProxyAcrossSnapshots(t *testing.T) {
	for _, provider := range []string{"devin", "meta", "kimi", "kimi-ai"} {
		t.Run(provider, func(t *testing.T) {
			h := newDevinProxyTestHandler(t, "http://global.invalid:8080")
			const selectedProxy = "socks5://selected.invalid:1080"
			fileName := provider + "-target.json"
			metadata := map[string]any{"type": provider, "proxy_url": selectedProxy}
			if provider == "kimi" {
				metadata["domain"] = "kimi.com"
			} else if provider == "kimi-ai" {
				metadata["domain"] = "kimi.ai"
			}
			_, errRegister := h.authManager.Register(context.Background(), &coreauth.Auth{
				ID: fileName, FileName: fileName, Provider: provider, ProxyURL: selectedProxy,
				Metadata: metadata, Attributes: map[string]string{coreauth.AttributePath: filepath.Join(h.cfg.AuthDir, fileName)},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			var route string
			oldDevin, oldMeta, oldKimi := newDevinOAuthService, newMetaOAuthService, newKimiOAuthService
			newDevinOAuthService = func(cfg *config.Config) devinOAuthService {
				route = cfg.ProxyURL
				return &fakeDevinOAuthService{}
			}
			newMetaOAuthService = func(cfg *config.Config) metaOAuthService {
				route = cfg.ProxyURL
				return &fakeMetaOAuthService{proceed: make(chan struct{})}
			}
			newKimiOAuthService = func(cfg *config.Config, domain string) kimiOAuthService {
				route = cfg.ProxyURL
				return &fakeKimiProxyOAuth{domain: domain}
			}
			t.Cleanup(func() {
				newDevinOAuthService, newMetaOAuthService, newKimiOAuthService = oldDevin, oldMeta, oldKimi
			})
			router := gin.New()
			router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
			body, errMarshal := json.Marshal(map[string]any{"provider": provider, "name": fileName})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("start targeted OAuth: %d %s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				State  string `json:"state"`
				Target string `json:"target_auth_id"`
			}
			if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
				t.Fatal(errDecode)
			}
			t.Cleanup(func() { CancelOAuthSession(response.State) })
			if route != selectedProxy || GetOAuthSessionProxyURL(response.State) != selectedProxy || response.Target != fileName {
				t.Fatal("targeted OAuth lost the selected credential route or target binding")
			}
			if h.cfg.ProxyURL != "http://global.invalid:8080" {
				t.Fatal("targeted OAuth changed the global proxy")
			}
		})
	}
}
