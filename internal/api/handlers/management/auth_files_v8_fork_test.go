package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func forkAuthV8Router(h *Handler) *gin.Engine {
	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/v8/management/oauth/callback", h.GetOAuthCallback)
	router.POST("/v8/management/oauth/callback", h.PostOAuthCallback)
	router.DELETE("/v8/management/oauth/session", h.CancelAuthSession)
	router.GET("/v8/management/credentials", h.ListAuthFiles)
	router.GET("/v8/management/credentials/concurrency", h.GetAuthFilesConcurrency)
	router.PATCH("/v8/management/credentials/fields", h.PatchAuthFileFields)
	return router
}

func forkAuthV8Request(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestOAuthV8RejectsInvalidProxyBeforeAcquisition(t *testing.T) {
	var constructed atomic.Int32
	original := newCodexOAuthService
	newCodexOAuthService = func(*config.Config) codexOAuthService { constructed.Add(1); return &fakeCodexOAuthService{} }
	t.Cleanup(func() { newCodexOAuthService = original })
	for _, tc := range []struct {
		name, policy, method, query, body string
		status                            int
	}{
		{"invalid-body", "prefer", http.MethodPost, "provider=codex", `{"proxy_url":"invalid://user:synthetic-secret@proxy.invalid"}`, 400},
		{"blank-body", "prefer", http.MethodPost, "provider=codex", `{"proxy_url":""}`, 400},
		{"proxy-query", "prefer", http.MethodGet, "provider=codex&proxy_url=direct", "", 400},
		{"required-global", "require", http.MethodPost, "provider=codex", `{}`, 409},
		{"unknown-credential", "prefer", http.MethodPost, "provider=codex", `{"auth_index":"missing"}`, 404},
		{"missing-provider", "prefer", http.MethodPost, "", `{"proxy_url":"direct"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCodexOAuthProxyTestHandler(t, "http://global.invalid:8080", tc.policy)
			response := forkAuthV8Request(forkAuthV8Router(h), tc.method, "/v8/management/oauth/auth-url?"+tc.query, tc.body)
			if response.Code != tc.status || strings.Contains(response.Body.String(), "synthetic-secret") {
				t.Fatalf("invalid route returned %d, want %d, or exposed proxy secret", response.Code, tc.status)
			}
		})
	}
	if constructed.Load() != 0 {
		t.Fatal("invalid selection constructed an OAuth client")
	}
}

type reauthV8Service struct {
	fakeCodexOAuthService
	proxy     string
	exchanged chan<- string
}

func (service *reauthV8Service) ExchangeCodeForTokens(ctx context.Context, code string, pkce *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	service.exchanged <- service.proxy
	bundle, errExchange := service.fakeCodexOAuthService.ExchangeCodeForTokens(ctx, code, pkce)
	bundle.TokenData.AccountID = "selected-account"
	return bundle, errExchange
}

func TestOAuthV8ReauthenticationPreservesNamespaceProxyAndLiveLease(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "http://global.invalid:8080", "require")
	target := registerCodexOAuthTarget(t, h)
	release, errAcquire := h.authManager.AcquireCredentialConcurrency(context.Background(), target)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	defer release()
	exchanged, saved := make(chan string, 1), make(chan *coreauth.Auth, 1)
	original := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		return &reauthV8Service{proxy: cfg.ProxyURL, exchanged: exchanged}
	}
	t.Cleanup(func() { newCodexOAuthService = original })
	h.postAuthPersistHook = func(_ context.Context, auth *coreauth.Auth) error { saved <- auth.Clone(); return nil }
	router := forkAuthV8Router(h)
	response := forkAuthV8Request(router, http.MethodPost, "/v8/management/oauth/auth-url?provider=%20CODEX%20", fmt.Sprintf(`{"auth_index":%q}`, lockedAuthIndex(target)))
	var login struct{ State string }
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &login) != nil || login.State == "" {
		t.Fatal("v8 reauthentication did not start")
	}
	t.Cleanup(func() { CancelOAuthSession(login.State) })
	response = forkAuthV8Request(router, http.MethodPost, "/v8/management/oauth/callback", fmt.Sprintf(`{"state":%q,"code":"v8-reauth"}`, login.State))
	if response.Code != http.StatusOK {
		t.Fatalf("v8 callback without provider rejected: %d", response.Code)
	}
	select {
	case proxy := <-exchanged:
		if proxy != target.ProxyURL {
			t.Fatal("v8 reauthentication ignored credential proxy")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("v8 callback did not reach exchange")
	}
	select {
	case record := <-saved:
		if record.ID != target.ID || record.ProxyURL != target.ProxyURL || !codexIdentityFieldsEqual(record.Metadata, target.Metadata) {
			t.Fatal("v8 save lost file binding, proxy or namespace")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("v8 reauthentication did not save")
	}
	if inflight, limit := h.authManager.CredentialConcurrencySnapshot(target.ID); inflight != 1 || limit != 10 {
		t.Fatalf("reauthentication detached the lease: %d/%d", inflight, limit)
	}
	response = forkAuthV8Request(router, http.MethodPatch, "/v8/management/credentials/fields", fmt.Sprintf(`{"name":%q,"concurrency_limit":3}`, target.FileName))
	if response.Code != http.StatusOK {
		t.Fatalf("v8 credential patch failed: %d %s", response.Code, response.Body.String())
	}
	if inflight, limit := h.authManager.CredentialConcurrencySnapshot(target.ID); inflight != 1 || limit != 3 {
		t.Fatalf("v8 patch detached the lease: %d/%d", inflight, limit)
	}
	release()
	response = forkAuthV8Request(router, http.MethodGet, "/v8/management/credentials/concurrency?name="+target.FileName, "")
	var counter struct {
		Entries []struct {
			Inflight int `json:"concurrency_in_flight"`
			Limit    int `json:"concurrency_limit"`
		}
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &counter) != nil || len(counter.Entries) != 1 || counter.Entries[0].Inflight != 0 || counter.Entries[0].Limit != 3 {
		t.Fatal("v8 concurrency endpoint lost released lease or configured limit")
	}
}

func TestOAuthV8CancellationRejectsBothCallbackMethods(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	original := newCodexOAuthService
	newCodexOAuthService = func(*config.Config) codexOAuthService { return &fakeCodexOAuthService{} }
	t.Cleanup(func() { newCodexOAuthService = original })
	router := forkAuthV8Router(h)
	response := forkAuthV8Request(router, http.MethodPost, "/v8/management/oauth/auth-url?provider=codex", `{"proxy_url":"direct"}`)
	var login struct{ State string }
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &login) != nil || login.State == "" {
		t.Fatal("v8 login did not start")
	}
	t.Cleanup(func() { CancelOAuthSession(login.State) })
	response = forkAuthV8Request(router, http.MethodDelete, "/v8/management/oauth/session?state="+login.State, "")
	if response.Code != 200 || IsOAuthSessionPending(login.State, "codex") {
		t.Fatal("v8 cancellation left session pending")
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path, body := "/v8/management/oauth/callback", fmt.Sprintf(`{"state":%q,"code":"cancelled"}`, login.State)
		if method == http.MethodGet {
			path += "?state=" + login.State + "&code=cancelled"
			body = ""
		}
		if got := forkAuthV8Request(router, method, path, body).Code; got != http.StatusNotFound {
			t.Fatalf("cancelled callback %s returned %d", method, got)
		}
	}
	entries, errRead := os.ReadDir(h.cfg.AuthDir)
	if errRead != nil || len(entries) != 0 {
		t.Fatal("cancelled v8 callback created a callback or credential file")
	}
}

func TestV8ReadsExistingCredentialsWithoutMigrationOrLogin(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "http://global.invalid:8080", "prefer")
	const namespace = "b6e838e0-2303-4c48-959f-5da633c48bc9"
	files := map[string]string{
		"codex-fixed.json":  `{"type":"codex","access_token":"synthetic-old","refresh_token":"synthetic-refresh","account_id":"old-account","codex_identity_version":1,"codex_identity_namespace":"` + namespace + `","proxy_url":"socks5://selected.invalid:1080","priority":-2,"concurrency_limit":3,"custom_old_field":"keep"}`,
		"codex-legacy.json": `{"type":"codex","access_token":"synthetic-legacy","account_id":"legacy-account","proxy_url":"direct"}`,
		"meta-old.json":     `{"type":"meta","auth_kind":"oauth","access_token":"LLM|synthetic-old","api_key":"LLM|synthetic-old","dca_token":"dca:synthetic-old","proxy_url":"direct","is_subs_active":true,"subs_tier_name":"Existing Tier"}`,
	}
	for name, raw := range files {
		if errWrite := os.WriteFile(filepath.Join(h.cfg.AuthDir, name), []byte(raw), 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	if errLoad := h.authManager.Load(context.Background()); errLoad != nil {
		t.Fatal(errLoad)
	}
	synthesized, errSynth := synthesizer.NewFileSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: h.cfg, AuthDir: h.cfg.AuthDir, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if errSynth != nil || len(synthesized) != 3 {
		t.Fatal("watcher could not load all existing credential formats")
	}
	for _, auth := range append(h.authManager.List(), synthesized...) {
		if auth.FileName == "codex-fixed.json" && (auth.ProxyURL != "socks5://selected.invalid:1080" || auth.Metadata["codex_identity_namespace"] != namespace || coreauth.EffectiveConcurrencyLimit(auth) != 3 || auth.Metadata["priority"] != float64(-2)) {
			t.Fatal("existing credential controls changed on load")
		}
		if auth.FileName == "codex-legacy.json" && (auth.ProxyURL != "direct" || coreauth.EffectiveConcurrencyLimit(auth) != 10) {
			t.Fatal("legacy credential lost explicit direct or default 10")
		}
	}
	for _, auth := range synthesized {
		if auth.FileName == "codex-fixed.json" && auth.Attributes["priority"] != "-2" {
			t.Fatal("watcher lost the persisted file priority routing attribute")
		}
	}
	router := forkAuthV8Router(h)
	for _, path := range []string{"/v8/management/credentials", "/v8/management/credentials/concurrency"} {
		if got := forkAuthV8Request(router, http.MethodGet, path, "").Code; got != 200 {
			t.Fatalf("read existing credentials: %d", got)
		}
	}
	for name, raw := range files {
		data, errRead := os.ReadFile(filepath.Join(h.cfg.AuthDir, name))
		if errRead != nil || !bytes.Equal(data, []byte(raw)) {
			t.Fatalf("loading/listing rewrote existing credential %s", name)
		}
	}
}

func TestOAuthV8CallbackValidatesPendingStateAndProvider(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	router := forkAuthV8Router(h)
	state := codex.NewCredentialIdentityNamespace()
	RegisterOAuthSession(state, "codex")
	t.Cleanup(func() { CancelOAuthSession(state) })
	response := forkAuthV8Request(router, http.MethodGet, "/v8/management/oauth/callback?provider=claude&state="+state+"&code=synthetic", "")
	if response.Code != http.StatusBadRequest || !IsOAuthSessionPending(state, "codex") {
		t.Fatal("provider mismatch consumed a valid pending session")
	}
	entries, errRead := os.ReadDir(h.cfg.AuthDir)
	if errRead != nil || len(entries) != 0 {
		t.Fatal("invalid callback created a file")
	}
	response = forkAuthV8Request(router, http.MethodGet, "/v8/management/oauth/callback?state="+state+"&code=synthetic", "")
	if response.Code != http.StatusOK {
		t.Fatalf("valid callback could not infer provider: %d", response.Code)
	}
	raw, errRead := os.ReadFile(filepath.Join(h.cfg.AuthDir, ".oauth-codex-"+state+".oauth"))
	var callback map[string]string
	if errRead != nil || json.Unmarshal(raw, &callback) != nil || callback["state"] != state || callback["code"] != "synthetic" {
		t.Fatal("valid v8 callback did not bind its state and code")
	}
	CompleteOAuthSession(state)
	response = forkAuthV8Request(router, http.MethodPost, "/v8/management/oauth/callback", fmt.Sprintf(`{"state":%q,"code":"replay"}`, state))
	if response.Code != http.StatusConflict {
		t.Fatal("completed v8 callback accepted replay")
	}
}
