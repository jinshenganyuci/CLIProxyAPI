package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type devinOAuthService interface {
	BuildAuthorizationURL(redirectURI, codeChallenge, state string) string
	ExchangeCodeForToken(ctx context.Context, code, codeVerifier string) (string, error)
	CreateAuthRecord(ctx context.Context, token string) (*coreauth.Auth, error)
}

type devinOAuthProxyService struct {
	devinOAuthService
	proxyURL string
	explicit bool
	state    string
}

func (service *devinOAuthProxyService) CreateAuthRecord(ctx context.Context, token string) (*coreauth.Auth, error) {
	record, errRecord := service.devinOAuthService.CreateAuthRecord(ctx, token)
	if errRecord != nil {
		return nil, errRecord
	}
	// Bind before the generic metadata merge so storage can distinguish newly
	// acquired provider fields from previously stored user controls.
	errBind := devin.BindOAuthProxy(record, service.proxyURL, service.explicit, func() error {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		return guardOAuthSessionPendingForSave(service.state, "devin")
	})
	return record, errBind
}

var newDevinOAuthService = func(cfg *config.Config) devinOAuthService {
	client, errClient := devin.NewOAuthHTTPClient(cfg.ProxyURL)
	if errClient != nil {
		return nil
	}
	return devin.NewDevinAuthService(client)
}

// devinCallbackURL builds the required loopback callback URL for Devin OAuth.
// Devin's authorization page strictly validates that redirect_uri matches
// http://127.0.0.1:<port>/callback (http protocol, 127.0.0.1 host, and /callback path).
func (h *Handler) devinCallbackURL() (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	return fmt.Sprintf("http://127.0.0.1:%d/callback", h.cfg.Port), nil
}

// RequestDevinToken starts the same callback/status flow used by the other WebUI providers.
func (h *Handler) RequestDevinToken(c *gin.Context) {
	snapshot, explicitProxy, status, errPrepare := h.prepareDevinOAuthSession(c)
	if errPrepare != nil {
		c.JSON(status, gin.H{"error": errPrepare.Error()})
		return
	}
	redirectURI, errRedirect := snapshot.devinCallbackURL()
	if errRedirect != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
		return
	}
	pkceCodes, errPKCE := devin.GeneratePKCECodes()
	if errPKCE != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	authSvc := newDevinOAuthService(snapshot.cfg)
	if authSvc == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid Devin OAuth proxy transport"})
		return
	}
	authURL := authSvc.BuildAuthorizationURL(redirectURI, pkceCodes.CodeChallenge, state)
	RegisterOAuthSessionWithAuthDir(state, "devin", snapshot.cfg.AuthDir)
	proxyURL, authDir := snapshot.cfg.ProxyURL, snapshot.cfg.AuthDir
	authSvc = &devinOAuthProxyService{devinOAuthService: authSvc, proxyURL: proxyURL, explicit: explicitProxy, state: state}
	originalHook := snapshot.postAuthHook
	snapshot.postAuthHook = func(ctx context.Context, record *coreauth.Auth) error {
		storage := record.Storage
		if originalHook != nil {
			if errHook := originalHook(ctx, record); errHook != nil {
				return errHook
			}
		}
		if record.Metadata == nil {
			record.Metadata = make(map[string]any)
		}
		record.ProxyURL = proxyURL
		record.Metadata["proxy_url"] = proxyURL
		record.Storage = storage
		// Recheck after hooks, immediately before persistence, just as Codex does.
		return guardOAuthSessionPendingForSave(state, "devin")
	}
	// Do not inherit the HTTP request cancellation: login continues after returning the URL.
	ctx := PopulateAuthContext(context.Background(), c)
	go snapshot.completeDevinOAuth(ctx, authDir, state, pkceCodes.CodeVerifier, authSvc)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) prepareDevinOAuthSession(c *gin.Context) (*Handler, bool, int, error) {
	if _, supplied := c.Request.URL.Query()["proxy_url"]; supplied {
		return nil, false, http.StatusBadRequest, fmt.Errorf("send proxy_url in a POST JSON body, not in the URL")
	}
	var request struct {
		ProxyURL *string `json:"proxy_url"`
	}
	if c.Request.Method == http.MethodPost {
		if errBind := c.ShouldBindJSON(&request); errBind != nil {
			return nil, false, http.StatusBadRequest, fmt.Errorf("invalid OAuth request body")
		}
	}
	if h == nil {
		return nil, false, http.StatusServiceUnavailable, fmt.Errorf("OAuth handler unavailable")
	}
	h.mu.Lock()
	snapshot := &Handler{
		cfg: h.cfg.CloneForRuntime(), authManager: h.authManager, tokenStore: h.tokenStore,
		postAuthHook: h.postAuthHook, postAuthPersistHook: h.postAuthPersistHook, pluginHost: h.pluginHost,
	}
	h.mu.Unlock()
	if snapshot.cfg == nil {
		return nil, false, http.StatusServiceUnavailable, fmt.Errorf("OAuth config unavailable")
	}
	authDir, errResolve := util.ResolveAuthDir(snapshot.cfg.AuthDir)
	if errResolve != nil || strings.TrimSpace(authDir) == "" {
		return nil, false, http.StatusInternalServerError, fmt.Errorf("OAuth auth directory unavailable")
	}
	snapshot.cfg.AuthDir = authDir
	proxyURL := snapshot.cfg.ProxyURL
	if request.ProxyURL != nil {
		proxyURL = strings.TrimSpace(*request.ProxyURL)
		if proxyURL == "" {
			return nil, false, http.StatusBadRequest, fmt.Errorf("proxy_url must contain a proxy URL or explicit direct")
		}
	}
	resolved, errProxy := devin.ResolveOAuthProxy(proxyURL)
	if errProxy != nil {
		return nil, false, http.StatusBadRequest, errProxy
	}
	snapshot.cfg.ProxyURL = resolved
	return snapshot, request.ProxyURL != nil, http.StatusOK, nil
}

func (h *Handler) completeDevinOAuth(ctx context.Context, authDir, state, codeVerifier string, authSvc devinOAuthService) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	go watchOAuthSessionCancel(ctx, cancel, state, "devin")

	waitFile := filepath.Join(authDir, fmt.Sprintf(".oauth-devin-%s.oauth", state))
	defer func() {
		if errRemove := os.Remove(waitFile); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warn("failed to remove Devin OAuth callback file")
		}
	}()
	result, errWait := waitDevinOAuthCallback(ctx, waitFile, state)
	if errWait != nil {
		if IsOAuthSessionPending(state, "devin") {
			SetOAuthSessionError(state, errWait.Error())
		}
		return
	}
	if !IsOAuthSessionPending(state, "devin") {
		return
	}
	if result.State != state {
		SetOAuthSessionError(state, "State code error")
		return
	}
	if result.Error != "" {
		SetOAuthSessionError(state, "Devin authorization denied")
		return
	}
	if strings.TrimSpace(result.Code) == "" {
		SetOAuthSessionError(state, "Missing authorization code")
		return
	}

	token, errExchange := authSvc.ExchangeCodeForToken(ctx, result.Code, codeVerifier)
	if errExchange != nil || strings.TrimSpace(token) == "" {
		// Upstream errors can contain tokens or authorization codes; never expose them.
		if IsOAuthSessionPending(state, "devin") {
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
		}
		return
	}
	if !IsOAuthSessionPending(state, "devin") {
		return
	}
	record, errRecord := authSvc.CreateAuthRecord(ctx, token)
	if errRecord != nil {
		if IsOAuthSessionPending(state, "devin") {
			SetOAuthSessionError(state, "Failed to create Devin authentication record")
		}
		return
	}
	if errGuard := guardOAuthSessionPendingForSave(state, "devin"); errGuard != nil {
		return
	}
	if record != nil {
		if record.Attributes == nil {
			record.Attributes = make(map[string]string)
		}
		// The shared token store may be reconfigured while the browser is open.
		record.Attributes[coreauth.AttributePath] = filepath.Join(authDir, record.FileName)
	}
	if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
		if IsOAuthSessionPending(state, "devin") {
			if errors.Is(errSave, devin.ErrOAuthProxyMismatch) {
				SetOAuthSessionError(state, devin.ErrOAuthProxyMismatch.Error())
			} else {
				SetOAuthSessionError(state, "Failed to save authentication tokens")
			}
		}
		return
	}
	CompleteOAuthSession(state)
	log.Info("Devin authentication successful")
}

func waitDevinOAuthCallback(ctx context.Context, path, state string) (*oauthCallbackFilePayload, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !IsOAuthSessionPending(state, "devin") {
			return nil, errOAuthSessionNotPending
		}
		if ctx.Err() != nil {
			return nil, errors.New("Timeout waiting for OAuth callback")
		}
		data, errRead := os.ReadFile(path)
		if errRead == nil {
			var result oauthCallbackFilePayload
			if errDecode := json.Unmarshal(data, &result); errDecode != nil {
				return nil, errors.New("Invalid OAuth callback")
			}
			return &result, nil
		}
		if !errors.Is(errRead, os.ErrNotExist) {
			return nil, errors.New("Failed to read OAuth callback")
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("Timeout waiting for OAuth callback")
		case <-ticker.C:
		}
	}
}
