package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

type codexOAuthRequest struct {
	ProxyURL  *string `json:"proxy_url"`
	AuthIndex string  `json:"auth_index"`
	IsWebUI   *bool   `json:"is_webui"`
}

type codexOAuthSessionOptions struct {
	handler    *Handler
	owner      *Handler
	target     *coreauth.Auth
	targetPath string
	accountID  string
	proxyURL   string
	proxyMode  string
	isWebUI    bool
}

func (h *Handler) prepareCodexOAuthSession(c *gin.Context) (*codexOAuthSessionOptions, int, error) {
	if _, supplied := c.Request.URL.Query()["proxy_url"]; supplied {
		return nil, http.StatusBadRequest, fmt.Errorf("send proxy_url in a POST JSON body, not in the URL")
	}
	request := codexOAuthRequest{AuthIndex: strings.TrimSpace(c.Query("auth_index"))}
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
	options := &codexOAuthSessionOptions{handler: snapshot, owner: h, isWebUI: isWebUIRequest(c)}
	if request.IsWebUI != nil {
		options.isWebUI = *request.IsWebUI
	}
	if authIndex := strings.TrimSpace(request.AuthIndex); authIndex != "" {
		if snapshot.authManager != nil {
			for _, auth := range snapshot.authManager.List() {
				if auth != nil && lockedAuthIndex(auth) == authIndex {
					options.target = auth.Clone()
					break
				}
			}
		}
		if options.target == nil {
			return nil, http.StatusNotFound, fmt.Errorf("OAuth credential not found")
		}
		if !strings.EqualFold(strings.TrimSpace(options.target.Provider), "codex") || options.target.AuthKind() != coreauth.AuthKindOAuth {
			return nil, http.StatusBadRequest, fmt.Errorf("selected credential is not Codex OAuth")
		}
		options.targetPath = snapshot.codexCredentialIdentityPath(options.target)
		if options.targetPath == "" {
			return nil, http.StatusConflict, fmt.Errorf("selected credential is not backed by a writable auth file")
		}
		options.accountID = codexOAuthAccountID(options.target.Metadata)
		if options.accountID == "" {
			return nil, http.StatusConflict, fmt.Errorf("selected credential has no account ID for reauthentication")
		}
	}
	selectedProxy := ""
	if request.ProxyURL != nil {
		selectedProxy = strings.TrimSpace(*request.ProxyURL)
		if selectedProxy == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("proxy_url must contain a proxy URL or explicit direct")
		}
	} else if options.target != nil {
		selectedProxy = strings.TrimSpace(options.target.ProxyURL)
		if selectedProxy == "" {
			selectedProxy = authMetadataStringValue(options.target.Metadata, "proxy_url")
		}
	}
	if selectedProxy == "" {
		if strings.EqualFold(strings.TrimSpace(snapshot.cfg.Codex.CredentialProxyPolicy), config.CodexCredentialProxyPolicyRequire) {
			return nil, http.StatusConflict, fmt.Errorf("credential-proxy-policy require needs an explicit login proxy or direct")
		}
		selectedProxy = strings.TrimSpace(snapshot.cfg.ProxyURL)
		if selectedProxy == "" {
			selectedProxy = "direct"
		}
	}
	setting, errProxy := codex.ResolveProxySetting(selectedProxy, "")
	if errProxy != nil || setting.Mode == proxyutil.ModeInvalid || setting.Mode == proxyutil.ModeInherit {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid OAuth proxy URL")
	}
	options.proxyURL = setting.Raw
	options.proxyMode = "proxy"
	if setting.Mode == proxyutil.ModeDirect {
		options.proxyURL = "direct"
		options.proxyMode = "direct"
	}
	// The service and persistence handler each belong to this one OAuth state.
	// Global config reloads cannot change its selected outbound route or auth dir.
	snapshot.cfg.ProxyURL = options.proxyURL
	originalHook := snapshot.postAuthHook
	snapshot.postAuthHook = func(ctx context.Context, auth *coreauth.Auth) error {
		if originalHook != nil {
			if errHook := originalHook(ctx, auth); errHook != nil {
				return errHook
			}
		}
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.ProxyURL = options.proxyURL
		auth.Metadata["proxy_url"] = options.proxyURL
		return nil
	}
	return options, http.StatusOK, nil
}

func codexOAuthAccountID(metadata map[string]any) string {
	if accountID := authMetadataStringValue(metadata, "account_id"); accountID != "" {
		return accountID
	}
	if claims, errParse := codex.ParseJWTToken(authMetadataStringValue(metadata, "id_token")); errParse == nil && claims != nil {
		return strings.TrimSpace(claims.GetAccountID())
	}
	return ""
}

func (options *codexOAuthSessionOptions) save(ctx context.Context, state string, record *coreauth.Auth) (string, error) {
	options.owner.codexIdentityMu.Lock()
	defer options.owner.codexIdentityMu.Unlock()
	if errGuard := guardOAuthSessionPendingForSave(state, "codex"); errGuard != nil {
		return "", errGuard
	}
	if options.target != nil {
		current, exists := options.handler.authManager.GetByID(options.target.ID)
		if !exists || current.RegistrationEpoch != options.target.RegistrationEpoch || codexOAuthAccountID(current.Metadata) != options.accountID {
			return "", fmt.Errorf("selected credential changed during reauthentication")
		}
		raw, errRead := os.ReadFile(options.targetPath)
		if errRead != nil {
			return "", fmt.Errorf("selected credential is no longer available")
		}
		var metadata map[string]any
		if errParse := json.Unmarshal(raw, &metadata); errParse != nil || codexOAuthAccountID(metadata) != options.accountID {
			return "", fmt.Errorf("selected credential file changed during reauthentication")
		}
	}
	if record.Attributes == nil {
		record.Attributes = make(map[string]string)
	}
	path := options.targetPath
	if path == "" {
		path = filepath.Join(options.handler.cfg.AuthDir, record.FileName)
	}
	record.Attributes[coreauth.AttributePath] = path
	originalHook := options.handler.postAuthHook
	options.handler.postAuthHook = func(ctx context.Context, auth *coreauth.Auth) error {
		if originalHook != nil {
			if errHook := originalHook(ctx, auth); errHook != nil {
				return errHook
			}
		}
		// A hook may take time or cancel this flow itself. Check again after
		// hooks, immediately before saveTokenRecord calls the token store.
		return guardOAuthSessionPendingForSave(state, "codex")
	}
	defer func() { options.handler.postAuthHook = originalHook }()
	return options.handler.saveTokenRecord(ctx, record)
}

func (options *codexOAuthSessionOptions) record(service codexOAuthService, bundle *codex.CodexAuthBundle) (*coreauth.Auth, error) {
	if bundle == nil {
		return nil, fmt.Errorf("OAuth token exchange returned no credentials")
	}
	if options.target != nil && strings.TrimSpace(bundle.TokenData.AccountID) != options.accountID {
		return nil, fmt.Errorf("authenticated account does not match the selected credential")
	}
	claims, _ := codex.ParseJWTToken(bundle.TokenData.IDToken)
	planType, hashAccountID := "", ""
	if claims != nil {
		planType = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
		if accountID := claims.GetAccountID(); accountID != "" {
			digest := sha256.Sum256([]byte(accountID))
			hashAccountID = hex.EncodeToString(digest[:])[:8]
		}
	}
	tokenStorage := service.CreateTokenStorage(bundle)
	if tokenStorage == nil {
		return nil, fmt.Errorf("OAuth token storage unavailable")
	}
	fileName := codex.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
	authID := fileName
	if options.target != nil {
		authID = options.target.ID
		fileName = options.target.FileName
		if strings.TrimSpace(fileName) == "" {
			fileName = options.target.ID
		}
	}
	record := &coreauth.Auth{
		ID: authID, Provider: "codex", FileName: fileName, Storage: tokenStorage, ProxyURL: options.proxyURL,
		Metadata: map[string]any{"email": tokenStorage.Email, "account_id": tokenStorage.AccountID, "proxy_url": options.proxyURL},
	}
	if options.target != nil {
		copyCodexIdentityFields(record.Metadata, options.target.Metadata)
	}
	return record, nil
}
