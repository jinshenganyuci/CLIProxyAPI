package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Exercise the new recovery selectors through the public v8 handlers and the
// file store while a real admission lease is held on the existing credential.
func TestV848TargetedRecoveryPreservesForkIdentityAndLease(t *testing.T) {
	for _, selector := range []string{"name", "auth_id", "target_auth_id", "auth_index"} {
		t.Run(selector, func(t *testing.T) {
			h := newCodexOAuthProxyTestHandler(t, "http://unused-global.invalid:8080", "require")
			h.authManager.SetConfig(h.cfg)
			target := registerCodexOAuthTarget(t, h)
			target.Metadata["concurrency_limit"] = 3
			var err error
			target, err = h.authManager.Update(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			release, err := h.authManager.AcquireCredentialConcurrency(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			service := &targetedRecoveryTestCodexOAuthService{authDir: h.cfg.AuthDir, email: "same@example.test", accountID: "selected-account"}
			original := newCodexOAuthService
			newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
				if cfg.ProxyURL != "direct" {
					t.Errorf("selected direct route was not frozen: %q", cfg.ProxyURL)
				}
				return service
			}
			t.Cleanup(func() { newCodexOAuthService = original })
			router := gin.New()
			router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
			router.GET("/v8/management/oauth/status", h.GetAuthStatus)
			value := target.ID
			if selector == "auth_index" {
				value = lockedAuthIndex(target)
			}
			body, _ := json.Marshal(map[string]any{"provider": "codex", selector: value, "proxy_url": "direct"})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(body))))
			if rec.Code != 200 {
				t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
			}
			var start map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
				t.Fatal(err)
			}
			if start["target_auth_id"] != target.ID || start["proxy_mode"] != "direct" {
				t.Fatalf("target/route: %#v", start)
			}
			status := pollOAuthStatusUntilDone(t, router, start["state"].(string))
			if status["saved"] != true || status["adopted"] != true || status["inference_verified"] != false {
				t.Fatalf("recovery outcome: %#v", status)
			}
			current, ok := h.authManager.GetByID(target.ID)
			if !ok || current.RegistrationEpoch != target.RegistrationEpoch || current.ProxyURL != "direct" || !codexIdentityFieldsEqual(current.Metadata, target.Metadata) {
				t.Fatal("recovery replaced credential identity, epoch or selected proxy")
			}
			if n, limit := h.authManager.CredentialConcurrencySnapshot(target.ID); n != 1 || limit != 3 {
				t.Fatalf("held lease changed: %d/%d", n, limit)
			}
			persisted := readCodexIdentityTestFile(t, filepath.Join(h.cfg.AuthDir, target.FileName))
			if persisted["proxy_url"] != "direct" || !codexIdentityFieldsEqual(persisted, target.Metadata) {
				t.Fatal("disk identity/route changed")
			}
			files, err := filepath.Glob(filepath.Join(h.cfg.AuthDir, "*.json"))
			if err != nil || len(files) != 1 {
				t.Fatalf("recovery created another credential: %v %v", files, err)
			}
			for _, secret := range []string{"fresh-access-", "fresh-refresh-", "selected-account"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatal("start response leaked credential material")
				}
			}
		})
	}
}

func TestV848RecoveryTargetSelectorMismatchDoesNotStart(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "require")
	target := registerCodexOAuthTarget(t, h)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url?auth_index="+lockedAuthIndex(target), strings.NewReader(`{"provider":"codex","auth_index":"different-index","proxy_url":"direct"}`))
	h.StartOAuthV8(c)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("conflicting target accepted: %d %s", rec.Code, rec.Body.String())
	}
	if data, err := os.ReadFile(filepath.Join(h.cfg.AuthDir, target.FileName)); err != nil || strings.Contains(string(data), "fresh-access-") {
		t.Fatal("rejected recovery modified credential")
	}
}

func TestV848VerifyByIndexUsesSameCredentialLookup(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	target := registerCodexOAuthTarget(t, h)
	h.authManager.RegisterExecutor(&failingRefreshManagementExecutor{provider: "codex", streamPayloads: [][]byte{[]byte("data: {\"type\":\"response.completed\"}\n\n")}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(ConfigV8ContextKey, true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v8/management/credentials/verify", strings.NewReader(`{"auth_index":"`+lockedAuthIndex(target)+`","model":"synthetic-v848"}`))
	h.VerifyAuthFileV8(c)
	if rec.Code != 200 {
		t.Fatalf("verify by index: %d %s", rec.Code, rec.Body.String())
	}
	current, _ := h.authManager.GetByID(target.ID)
	if !current.InferenceVerified || current.RegistrationEpoch != target.RegistrationEpoch || coreauth.EffectiveConcurrencyLimit(current) != 10 {
		t.Fatal("verification changed identity or admission defaults")
	}
}

func TestV848ImmediateOAuthAdoptionKeepsRuntimeExtensionsWithoutOldHeaders(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	target := registerCodexOAuthTarget(t, h)
	target.Metadata["headers"] = map[string]any{"X-Old-Header": "old-value"}
	coreauth.ApplyCustomHeadersFromMetadata(target)
	var err error
	target, err = h.authManager.Update(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	target.Attributes["plugin-runtime-extension"] = "retained"
	if _, err = h.authManager.Update(coreauth.WithSkipPersist(context.Background()), target); err != nil {
		t.Fatal(err)
	}
	fresh := target.Clone()
	fresh.Metadata["access_token"] = "new-access"
	fresh.Metadata["headers"] = map[string]any{}
	if _, err = h.saveTokenRecord(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	adopted, ok := h.authManager.GetByID(target.ID)
	if !ok || adopted.Attributes["plugin-runtime-extension"] != "retained" || adopted.Metadata["access_token"] != "new-access" {
		t.Fatal("immediate adoption dropped runtime extension or canonical grant")
	}
	for key := range adopted.Attributes {
		if strings.HasPrefix(key, "header:") {
			t.Fatalf("removed file header was restored: %s", key)
		}
	}
	if adopted.RegistrationEpoch != target.RegistrationEpoch || adopted.ProxyURL != target.ProxyURL || !codexIdentityFieldsEqual(adopted.Metadata, target.Metadata) {
		t.Fatal("immediate adoption changed credential controls")
	}
}

func TestV848ImmediateOAuthAdoptionRejectsReplacementRegistration(t *testing.T) {
	h := newCodexOAuthProxyTestHandler(t, "", "prefer")
	target := registerCodexOAuthTarget(t, h)
	target.Attributes["old-runtime-extension"] = "must-not-transfer"
	if _, err := h.authManager.Update(coreauth.WithSkipPersist(context.Background()), target); err != nil {
		t.Fatal(err)
	}
	var replacement *coreauth.Auth
	h.postAuthHook = func(ctx context.Context, _ *coreauth.Auth) error {
		h.authManager.Remove(ctx, target.ID)
		next := target.Clone()
		next.Attributes = map[string]string{coreauth.AttributePath: filepath.Join(h.cfg.AuthDir, target.FileName), "new-runtime-extension": "keep"}
		next.Metadata["access_token"] = "replacement-token"
		var err error
		replacement, err = h.authManager.Register(coreauth.WithSkipPersist(ctx), next)
		return err
	}
	fresh := target.Clone()
	fresh.Metadata["access_token"] = "oauth-token"
	path, err := h.saveTokenRecord(context.Background(), fresh)
	if err == nil || path == "" {
		t.Fatal("expected saved-but-not-adopted result after registration replacement")
	}
	current, ok := h.authManager.GetByID(target.ID)
	if !ok || current.RegistrationEpoch != replacement.RegistrationEpoch || current.Metadata["access_token"] != "replacement-token" || current.Attributes["new-runtime-extension"] != "keep" || current.Attributes["old-runtime-extension"] != "" {
		t.Fatal("late OAuth adoption changed the replacement registration")
	}
}
