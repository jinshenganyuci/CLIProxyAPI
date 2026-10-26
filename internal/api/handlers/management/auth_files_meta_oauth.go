package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type metaOAuthService interface {
	StartDeviceFlow(context.Context) (*metaauth.DeviceCodeResponse, error)
	WaitForAuthorization(context.Context, *metaauth.DeviceCodeResponse) (*metaauth.MetaAuthBundle, error)
	CreateTokenStorage(*metaauth.MetaAuthBundle) *metaauth.MetaTokenStorage
}

var newMetaOAuthService = func(cfg *config.Config) metaOAuthService {
	return metaauth.NewMetaAuth(cfg)
}

// RequestMetaToken binds all server-side device authorization requests to one route.
func (h *Handler) RequestMetaToken(c *gin.Context) {
	snapshot, explicitProxy, status, errPrepare := h.prepareMetaOAuthSession(c)
	if errPrepare != nil {
		c.JSON(status, gin.H{"error": errPrepare.Error()})
		return
	}
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}
	// Device polling must continue after the initial management request completes.
	ctx := PopulateAuthContext(context.Background(), c)
	authSvc := newMetaOAuthService(snapshot.cfg)
	deviceFlow, errStart := authSvc.StartDeviceFlow(ctx)
	if errStart != nil || deviceFlow == nil {
		// Upstream and transport errors may contain credentials or device codes.
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(deviceFlow.VerificationURI)
	}
	registerOAuthSessionWithOptions(state, "meta", snapshot.cfg.AuthDir, snapshot.cfg.ProxyURL)
	proxyURL := snapshot.cfg.ProxyURL
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
		return claimOAuthSessionCommit(state, "meta")
	}
	go snapshot.completeMetaOAuth(ctx, state, explicitProxy, authSvc, deviceFlow)
	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	} else {
		response["expires_in"] = int(metaauth.MaxPollDuration / time.Second)
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) prepareMetaOAuthSession(c *gin.Context) (*Handler, bool, int, error) {
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
	resolved, errProxy := metaauth.ResolveOAuthProxy(proxyURL)
	if errProxy != nil {
		return nil, false, http.StatusBadRequest, errProxy
	}
	snapshot.cfg.ProxyURL = resolved
	return snapshot, request.ProxyURL != nil, http.StatusOK, nil
}

func (h *Handler) completeMetaOAuth(ctx context.Context, state string, explicit bool, authSvc metaOAuthService, deviceFlow *metaauth.DeviceCodeResponse) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go watchOAuthSessionCancel(ctx, cancel, state, "meta")
	bundle, errWait := authSvc.WaitForAuthorization(ctx, deviceFlow)
	if errWait != nil {
		if IsOAuthSessionPending(state, "meta") {
			SetOAuthSessionError(state, "Authentication failed")
		}
		return
	}
	guard := func() error {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		return guardOAuthSessionPendingForSave(state, "meta")
	}
	if guard() != nil {
		return
	}
	record := buildMetaAuthRecord(bundle, authSvc.CreateTokenStorage(bundle))
	if record == nil {
		SetOAuthSessionError(state, "Failed to create Meta authentication record")
		return
	}
	if errBind := metaauth.BindOAuthProxy(record, h.cfg.ProxyURL, explicit, guard); errBind != nil {
		SetOAuthSessionError(state, "Failed to bind Meta authentication route")
		return
	}
	// The shared store may be reconfigured while device authorization is pending.
	record.Attributes[coreauth.AttributePath] = filepath.Join(h.cfg.AuthDir, record.FileName)
	if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
		if IsOAuthSessionPending(state, "meta") {
			if errors.Is(errSave, metaauth.ErrOAuthProxyMismatch) {
				FailOAuthSessionCommit(state, metaauth.ErrOAuthProxyMismatch.Error())
			} else {
				FailOAuthSessionCommit(state, "Failed to save authentication tokens")
			}
		}
		return
	}
	CompleteOAuthSession(state)
	log.Info("Meta authentication successful")
}

// buildMetaAuthRecord includes acquired subscription state before route storage snapshots metadata.
func buildMetaAuthRecord(bundle *metaauth.MetaAuthBundle, tokenStorage *metaauth.MetaTokenStorage) *coreauth.Auth {
	record, errRecord := metaauth.CreateAuthRecord(tokenStorage)
	if errRecord != nil {
		return nil
	}
	if bundle != nil && bundle.MintedKey != nil {
		record.Metadata["subs_tier_name"] = bundle.MintedKey.SubsTierName
		record.Metadata["subs_tier_id"] = bundle.MintedKey.SubsTierID
		record.Metadata["is_subs_active"] = bundle.MintedKey.IsSubsActive
		record.Metadata["has_payment_method"] = bundle.MintedKey.HasPaymentMethod
	}
	return record
}
