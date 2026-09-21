package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

type kimiOAuthService interface {
	StartDeviceFlow(context.Context) (*kimi.DeviceCodeResponse, error)
	WaitForAuthorization(context.Context, *kimi.DeviceCodeResponse) (*kimi.KimiAuthBundle, error)
	CreateTokenStorage(*kimi.KimiAuthBundle) *kimi.KimiTokenStorage
}

var newKimiOAuthService = func(cfg *config.Config, domain string) kimiOAuthService {
	return kimi.NewKimiAuthWithDomain(cfg, domain)
}

func (h *Handler) prepareKimiOAuthSession(c *gin.Context) (*Handler, int, error) {
	if _, supplied := c.Request.URL.Query()["proxy_url"]; supplied {
		return nil, http.StatusBadRequest, fmt.Errorf("send proxy_url in a POST JSON body, not in the URL")
	}
	var request struct {
		ProxyURL *string `json:"proxy_url"`
	}
	if c.Request.Method == http.MethodPost {
		if errBind := c.ShouldBindJSON(&request); errBind != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid OAuth request body")
		}
	}
	if h == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("OAuth handler unavailable")
	}
	h.mu.Lock()
	snapshot := &Handler{
		cfg: h.cfg.CloneForRuntime(), authManager: h.authManager, tokenStore: h.tokenStore,
		postAuthHook: h.postAuthHook, postAuthPersistHook: h.postAuthPersistHook, pluginHost: h.pluginHost,
	}
	h.mu.Unlock()
	if snapshot.cfg == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("OAuth config unavailable")
	}
	authDir, errResolve := util.ResolveAuthDir(snapshot.cfg.AuthDir)
	if errResolve != nil || strings.TrimSpace(authDir) == "" {
		return nil, http.StatusInternalServerError, fmt.Errorf("OAuth auth directory unavailable")
	}
	snapshot.cfg.AuthDir = authDir
	proxyURL := snapshot.cfg.ProxyURL
	if request.ProxyURL != nil {
		proxyURL = strings.TrimSpace(*request.ProxyURL)
		if proxyURL == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("proxy_url must contain a proxy URL or explicit direct")
		}
	}
	resolved, errProxy := kimi.ResolveOAuthProxy(proxyURL)
	if errProxy != nil {
		return nil, http.StatusBadRequest, errProxy
	}
	snapshot.cfg.ProxyURL = resolved
	return snapshot, http.StatusOK, nil
}
