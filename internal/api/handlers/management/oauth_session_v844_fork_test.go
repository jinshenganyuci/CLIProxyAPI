package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV844OAuthBodyPreservesSelectedCredentialAndAlias(t *testing.T) {
	original := newCodexOAuthService
	defer func() { newCodexOAuthService = original }()
	for _, tc := range []struct{ name, field, value, want string }{
		{"selected-route", "", "", "socks5://old.example:1080"},
		{"explicit-direct", "proxy_url", "direct", "direct"},
		{"native-alias", "proxy-url", "http://user:synthetic-secret@proxy.invalid:8080", "http://user:synthetic-secret@proxy.invalid:8080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCodexOAuthProxyTestHandler(t, "http://global.invalid:8080", "require")
			target := registerCodexOAuthTarget(t, h)
			captured := make(chan *config.Config, 1)
			newCodexOAuthService = func(cfg *config.Config) codexOAuthService { captured <- cfg; return &fakeCodexOAuthService{} }
			body := map[string]any{"provider": "codex", "auth_index": lockedAuthIndex(target), "is_webui": false}
			if tc.field != "" {
				body[tc.field] = tc.value
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.POST("/start", h.StartOAuthV8)
			router.GET("/status", h.GetAuthStatus)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/start", strings.NewReader(string(encoded))))
			if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "synthetic-secret") {
				t.Fatalf("start failed or exposed route: status=%d", rec.Code)
			}
			var response struct {
				State string `json:"state"`
			}
			if err = json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			cfg := <-captured
			if cfg.ProxyURL != tc.want || GetOAuthSessionProxyURL(response.State) != tc.want {
				t.Fatal("body selector or native alias changed selected route")
			}
			session, ok := oauthSessions.Get(response.State)
			if !ok || session.CallbackAuthDir != h.cfg.AuthDir {
				t.Fatal("callback lost original auth directory")
			}
			if _, errCallback := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, "codex", response.State, "different-account", ""); errCallback != nil {
				t.Fatal(errCallback)
			}
			if status := waitForOAuthStatus(t, router, response.State); status.Status != "error" {
				t.Fatal("selected account fence was lost through native body dispatch")
			}
			if GetOAuthSessionProxyURL(response.State) != "" {
				t.Fatal("failed account check retained session route")
			}
		})
	}
}

func TestV844OAuthCommitClaimAfterHookBeforeStore(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "require")
	target := registerCodexOAuthTarget(t, h)
	state := "v844-commit-boundary"
	hookEntered := make(chan struct{})
	releaseHook := make(chan struct{})
	h.postAuthHook = func(context.Context, *coreauth.Auth) error { close(hookEntered); <-releaseHook; return nil }
	options, status, err := prepareCodexOAuthProxyTest(t, h, http.MethodPost, "/start", `{"auth_index":"`+lockedAuthIndex(target)+`"}`)
	if err != nil || status != 200 {
		t.Fatal("prepare failed")
	}
	record := target.Clone()
	RegisterOAuthSessionWithProxy(state, "codex", target.ProxyURL)
	done := make(chan error, 1)
	go func() { _, errSave := options.save(context.Background(), state, record); done <- errSave }()
	<-hookEntered
	if !CancelOAuthSession(state) {
		t.Fatal("cancel before store commit lost")
	}
	close(releaseHook)
	if <-done == nil {
		t.Fatal("cancelled flow persisted")
	}
	RegisterOAuthSessionWithProxy(state, "codex", target.ProxyURL)
	if errCommit := claimOAuthSessionCommit(state, "codex"); errCommit != nil {
		t.Fatal(errCommit)
	}
	if CancelOAuthSession(state) {
		t.Fatal("cancel won after commit claim")
	}
	FailOAuthSessionCommit(state, "failed via "+target.ProxyURL)
	if GetOAuthSessionProxyURL(state) != "" {
		t.Fatal("failed commit retained route")
	}
}
