package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/credentialfile"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

type codexCredentialIdentityItem struct {
	Name          string `json:"name"`
	AuthIndex     string `json:"auth_index,omitempty"`
	Email         string `json:"email,omitempty"`
	Status        string `json:"status"`
	IdentityHash  string `json:"identity_hash,omitempty"`
	ProxyMode     string `json:"proxy_mode"`
	SourceBackend string `json:"source_backend,omitempty"`
	Writable      bool   `json:"writable"`
	Message       string `json:"message,omitempty"`
}

type codexCredentialIdentityStatus struct {
	Enabled                         bool                          `json:"enabled"`
	SynthesizeMissingInstallationID bool                          `json:"synthesize_missing_installation_id"`
	CredentialProxyPolicy           string                        `json:"credential_proxy_policy"`
	LegacyIdentityConfuse           bool                          `json:"legacy_identity_confuse"`
	Total                           int                           `json:"total"`
	Ready                           int                           `json:"ready"`
	Missing                         int                           `json:"missing"`
	Invalid                         int                           `json:"invalid"`
	Conflicts                       int                           `json:"conflicts"`
	Unsupported                     int                           `json:"unsupported"`
	ProxyMissing                    int                           `json:"proxy_missing"`
	AllReady                        bool                          `json:"all_ready"`
	Credentials                     []codexCredentialIdentityItem `json:"credentials"`
}

type codexCredentialIdentityMutation struct {
	auth        *coreauth.Auth
	path        string
	originalRaw []byte
	updatedRaw  []byte
	namespace   string
}

var (
	errCodexIdentityConflict = errors.New("Codex credential identity transaction conflict")
	errCodexIdentityRollback = errors.New("Codex credential identity rollback failed")
)

// GetCodexCredentialIdentity reports migration readiness without exposing namespaces or tokens.
func (h *Handler) GetCodexCredentialIdentity(c *gin.Context) {
	status, errStatus := h.codexCredentialIdentityStatus(c.Request.Context())
	if errStatus != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errStatus.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}

// PutCodexCredentialIdentity updates the global feature switches after validating readiness.
func (h *Handler) PutCodexCredentialIdentity(c *gin.Context) {
	var req struct {
		Enabled                         *bool   `json:"enabled"`
		SynthesizeMissingInstallationID *bool   `json:"synthesize_missing_installation_id"`
		CredentialProxyPolicy           *string `json:"credential_proxy_policy"`
	}
	if errBind := c.ShouldBindJSON(&req); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	h.codexIdentityMu.Lock()
	defer h.codexIdentityMu.Unlock()

	if req.Enabled != nil && *req.Enabled {
		status, errStatus := h.codexCredentialIdentityStatus(c.Request.Context())
		if errStatus != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": errStatus.Error()})
			return
		}
		if !status.AllReady {
			c.JSON(http.StatusConflict, gin.H{
				"error":  "all Codex OAuth credentials must have valid unique identity namespaces before enabling",
				"status": status,
			})
			return
		}
	}

	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config unavailable"})
		return
	}
	previous := h.cfg.Codex
	if req.Enabled != nil {
		h.cfg.Codex.CredentialIdentity.Enabled = *req.Enabled
	}
	if req.SynthesizeMissingInstallationID != nil {
		h.cfg.Codex.CredentialIdentity.SynthesizeMissingInstallationID = *req.SynthesizeMissingInstallationID
	}
	if req.CredentialProxyPolicy != nil {
		h.cfg.Codex.CredentialProxyPolicy = *req.CredentialProxyPolicy
	}
	if errValidate := h.cfg.NormalizeAndValidateCodex(); errValidate != nil {
		h.cfg.Codex = previous
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": errValidate.Error()})
		return
	}
	snapshot, saved := h.saveConfigAndSnapshotLocked(c)
	if !saved {
		h.cfg.Codex = previous
		h.mu.Unlock()
		return
	}
	current := h.cfg.Codex
	h.mu.Unlock()
	h.reloadConfigAfterManagementSave(c.Request.Context(), snapshot)

	c.JSON(http.StatusOK, gin.H{
		"status":                             "ok",
		"enabled":                            current.CredentialIdentity.Enabled,
		"synthesize_missing_installation_id": current.CredentialIdentity.SynthesizeMissingInstallationID,
		"credential_proxy_policy":            current.CredentialProxyPolicy,
	})
}

// InitializeCodexCredentialIdentity initializes every missing writable Codex OAuth namespace.
func (h *Handler) InitializeCodexCredentialIdentity(c *gin.Context) {
	h.codexIdentityMu.Lock()
	defer h.codexIdentityMu.Unlock()

	auths, errList := h.codexOAuthCredentials(c.Request.Context())
	if errList != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errList.Error()})
		return
	}

	mutations, errPlan := h.planCodexCredentialIdentityInitialization(auths)
	if errPlan != nil {
		c.JSON(http.StatusConflict, gin.H{"error": errPlan.Error()})
		return
	}
	if len(mutations) == 0 {
		status, errStatus := h.codexCredentialIdentityStatusForAuths(auths)
		if errStatus != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": errStatus.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "initialized": 0, "identity": status})
		return
	}

	if errApply := h.applyCodexCredentialIdentityMutations(c.Request.Context(), mutations); errApply != nil {
		if codexIdentityMutationConflict(errApply) {
			c.JSON(http.StatusConflict, gin.H{"error": errApply.Error()})
			return
		}
		h.disableCodexCredentialIdentityAfterMigrationFailure(c.Request.Context())
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":            errApply.Error(),
			"feature_disabled": true,
		})
		return
	}

	status, errStatus := h.codexCredentialIdentityStatus(c.Request.Context())
	if errStatus != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errStatus.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "initialized": len(mutations), "identity": status})
}

// RotateCodexCredentialIdentity replaces one namespace after explicit confirmation.
func (h *Handler) RotateCodexCredentialIdentity(c *gin.Context) {
	var req struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		Confirm   string `json:"confirm"`
	}
	if errBind := c.ShouldBindJSON(&req); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.Confirm != "ROTATE" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirm must be ROTATE"})
		return
	}

	h.codexIdentityMu.Lock()
	defer h.codexIdentityMu.Unlock()

	auths, errList := h.codexOAuthCredentials(c.Request.Context())
	if errList != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errList.Error()})
		return
	}
	var target *coreauth.Auth
	for _, auth := range auths {
		if matchesAuthFileLookup(auth, req.Name, req.AuthIndex) {
			target = auth
			break
		}
	}
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Codex OAuth credential not found"})
		return
	}

	mutation, errMutation := h.planCodexCredentialIdentityMutation(target, codex.NewCredentialIdentityNamespace(), true)
	if errMutation != nil {
		c.JSON(http.StatusConflict, gin.H{"error": errMutation.Error()})
		return
	}
	if errApply := h.applyCodexCredentialIdentityMutations(c.Request.Context(), []codexCredentialIdentityMutation{mutation}); errApply != nil {
		if codexIdentityMutationConflict(errApply) {
			c.JSON(http.StatusConflict, gin.H{"error": errApply.Error()})
			return
		}
		h.disableCodexCredentialIdentityAfterMigrationFailure(c.Request.Context())
		c.JSON(http.StatusInternalServerError, gin.H{"error": errApply.Error(), "feature_disabled": true})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":         "ok",
		"name":           target.FileName,
		"auth_index":     lockedAuthIndex(target),
		"identity_hash":  codexCredentialIdentityHash(mutation.namespace),
		"sessions_reset": true,
	})
}

func (h *Handler) codexCredentialIdentityStatus(ctx context.Context) (codexCredentialIdentityStatus, error) {
	auths, errList := h.codexOAuthCredentials(ctx)
	if errList != nil {
		return codexCredentialIdentityStatus{}, errList
	}
	return h.codexCredentialIdentityStatusForAuths(auths)
}

func (h *Handler) codexCredentialIdentityStatusForAuths(auths []*coreauth.Auth) (codexCredentialIdentityStatus, error) {
	status := codexCredentialIdentityStatus{Credentials: make([]codexCredentialIdentityItem, 0, len(auths))}
	if h != nil {
		h.mu.Lock()
		if h.cfg != nil {
			codexConfig := h.cfg.Codex
			status.Enabled = codexConfig.CredentialIdentity.Enabled
			status.SynthesizeMissingInstallationID = codexConfig.CredentialIdentity.SynthesizeMissingInstallationID
			status.CredentialProxyPolicy = codexConfig.CredentialProxyPolicy
			status.LegacyIdentityConfuse = codexConfig.IdentityConfuse
		}
		h.mu.Unlock()
	}
	if strings.TrimSpace(status.CredentialProxyPolicy) == "" {
		status.CredentialProxyPolicy = config.CodexCredentialProxyPolicyPrefer
	}

	type parsedIdentity struct {
		index     int
		namespace string
	}
	parsed := make([]parsedIdentity, 0, len(auths))
	for _, auth := range auths {
		item := codexCredentialIdentityItem{
			Name:          auth.FileName,
			AuthIndex:     lockedAuthIndex(auth),
			Email:         authMetadataStringValue(auth.Metadata, "email"),
			ProxyMode:     codexCredentialProxyMode(auth.ProxyURL),
			SourceBackend: auth.AuthSourceKind(),
			Writable:      h.codexCredentialIdentityPath(auth) != "",
		}
		if item.Name == "" {
			item.Name = auth.ID
		}
		if item.ProxyMode == "inherit" {
			status.ProxyMissing++
		}

		_, hasVersion := auth.Metadata[codex.CredentialIdentityVersionMetadataKey]
		_, hasNamespace := auth.Metadata[codex.CredentialIdentityNamespaceMetadataKey]
		switch {
		case !hasVersion && !hasNamespace:
			item.Status = "missing"
			status.Missing++
			if !item.Writable {
				status.Unsupported++
				item.Message = "credential backend cannot be migrated by this endpoint"
			}
		default:
			namespace, _, errParse := codex.ParseCredentialIdentity(auth.Metadata)
			if errParse != nil {
				item.Status = "invalid"
				item.Message = errParse.Error()
				status.Invalid++
			} else {
				item.Status = "ready"
				item.IdentityHash = codexCredentialIdentityHash(namespace.String())
				parsed = append(parsed, parsedIdentity{index: len(status.Credentials), namespace: namespace.String()})
			}
		}
		status.Credentials = append(status.Credentials, item)
	}

	namespaceOwners := make(map[string][]int)
	for _, identity := range parsed {
		namespaceOwners[identity.namespace] = append(namespaceOwners[identity.namespace], identity.index)
	}
	for _, owners := range namespaceOwners {
		if len(owners) < 2 {
			continue
		}
		for _, index := range owners {
			status.Credentials[index].Status = "conflict"
			status.Credentials[index].Message = "identity namespace is shared by multiple credentials"
			status.Conflicts++
		}
	}

	status.Total = len(status.Credentials)
	for _, item := range status.Credentials {
		if item.Status == "ready" {
			status.Ready++
		}
	}
	status.AllReady = status.Ready == status.Total && status.Missing == 0 && status.Invalid == 0 && status.Conflicts == 0
	sort.Slice(status.Credentials, func(i, j int) bool {
		return strings.ToLower(status.Credentials[i].Name) < strings.ToLower(status.Credentials[j].Name)
	})
	return status, nil
}

func (h *Handler) codexOAuthCredentials(ctx context.Context) ([]*coreauth.Auth, error) {
	var auths []*coreauth.Auth
	if h != nil && h.authManager != nil {
		auths = h.authManager.List()
	} else {
		store := h.tokenStoreWithBaseDir()
		if store == nil {
			return nil, fmt.Errorf("token store unavailable")
		}
		listed, errList := store.List(ctx)
		if errList != nil {
			return nil, fmt.Errorf("list auth records: %w", errList)
		}
		auths = listed
	}

	filtered := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		if auth.AuthKind() != coreauth.AuthKindOAuth {
			continue
		}
		filtered = append(filtered, auth)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	return filtered, nil
}

func (h *Handler) planCodexCredentialIdentityInitialization(auths []*coreauth.Auth) ([]codexCredentialIdentityMutation, error) {
	seen := make(map[string]string, len(auths))
	missing := make([]*coreauth.Auth, 0)
	for _, auth := range auths {
		_, hasVersion := auth.Metadata[codex.CredentialIdentityVersionMetadataKey]
		_, hasNamespace := auth.Metadata[codex.CredentialIdentityNamespaceMetadataKey]
		if !hasVersion && !hasNamespace {
			if h.codexCredentialIdentityPath(auth) == "" {
				return nil, fmt.Errorf("Codex OAuth credential %q is not backed by a writable auth file", auth.ID)
			}
			missing = append(missing, auth)
			continue
		}
		namespace, _, errParse := codex.ParseCredentialIdentity(auth.Metadata)
		if errParse != nil {
			return nil, fmt.Errorf("Codex OAuth credential %q: %w", auth.ID, errParse)
		}
		if owner, exists := seen[namespace.String()]; exists {
			return nil, fmt.Errorf("Codex OAuth credentials %q and %q share one identity namespace", owner, auth.ID)
		}
		seen[namespace.String()] = auth.ID
	}

	mutations := make([]codexCredentialIdentityMutation, 0, len(missing))
	for _, auth := range missing {
		var namespace string
		for {
			namespace = codex.NewCredentialIdentityNamespace()
			if _, exists := seen[namespace]; !exists {
				break
			}
		}
		mutation, errMutation := h.planCodexCredentialIdentityMutation(auth, namespace, false)
		if errMutation != nil {
			return nil, errMutation
		}
		seen[namespace] = auth.ID
		mutations = append(mutations, mutation)
	}
	return mutations, nil
}

func (h *Handler) planCodexCredentialIdentityMutation(auth *coreauth.Auth, namespace string, replace bool) (codexCredentialIdentityMutation, error) {
	path := h.codexCredentialIdentityPath(auth)
	if path == "" {
		return codexCredentialIdentityMutation{}, fmt.Errorf("Codex OAuth credential %q is not backed by a writable auth file", auth.ID)
	}
	originalRaw, errRead := os.ReadFile(path)
	if errRead != nil {
		return codexCredentialIdentityMutation{}, fmt.Errorf("read Codex OAuth credential %q: %w", auth.ID, errRead)
	}
	metadata := make(map[string]any)
	if errUnmarshal := json.Unmarshal(originalRaw, &metadata); errUnmarshal != nil {
		return codexCredentialIdentityMutation{}, fmt.Errorf("parse Codex OAuth credential %q: %w", auth.ID, errUnmarshal)
	}
	if !strings.EqualFold(authMetadataStringValue(metadata, "type"), "codex") {
		return codexCredentialIdentityMutation{}, fmt.Errorf("credential %q is not a Codex auth file", auth.ID)
	}
	if !replace {
		if _, hasVersion := metadata[codex.CredentialIdentityVersionMetadataKey]; hasVersion {
			return codexCredentialIdentityMutation{}, fmt.Errorf("Codex OAuth credential %q already has identity metadata", auth.ID)
		}
		if _, hasNamespace := metadata[codex.CredentialIdentityNamespaceMetadataKey]; hasNamespace {
			return codexCredentialIdentityMutation{}, fmt.Errorf("Codex OAuth credential %q has partial identity metadata", auth.ID)
		}
	}
	if errSet := codex.SetCredentialIdentity(metadata, namespace); errSet != nil {
		return codexCredentialIdentityMutation{}, errSet
	}
	updatedRaw, errMarshal := json.MarshalIndent(metadata, "", "  ")
	if errMarshal != nil {
		return codexCredentialIdentityMutation{}, fmt.Errorf("marshal Codex OAuth credential %q: %w", auth.ID, errMarshal)
	}
	updatedRaw = append(updatedRaw, '\n')
	return codexCredentialIdentityMutation{
		auth:        auth,
		path:        path,
		originalRaw: originalRaw,
		updatedRaw:  updatedRaw,
		namespace:   namespace,
	}, nil
}

func (h *Handler) applyCodexCredentialIdentityMutations(ctx context.Context, mutations []codexCredentialIdentityMutation) error {
	committed := make([]codexCredentialIdentityMutation, 0, len(mutations))
	for _, mutation := range mutations {
		if errCommit := h.applyCodexCredentialIdentityMutation(ctx, mutation, false); errCommit != nil {
			// Validation conflicts happen before writing. Other errors may occur
			// after rename, so include the attempted commit in their rollback.
			if !errors.Is(errCommit, errCodexIdentityConflict) {
				committed = append(committed, mutation)
			}
			combined := fmt.Errorf("commit Codex credential identity for %q: %w", mutation.auth.ID, errCommit)
			for index := len(committed) - 1; index >= 0; index-- {
				if errRollback := h.applyCodexCredentialIdentityMutation(context.Background(), committed[index], true); errRollback != nil {
					combined = errors.Join(combined, errCodexIdentityRollback, fmt.Errorf("rollback Codex credential identity for %q: %w", committed[index].auth.ID, errRollback))
				}
			}
			return combined
		}
		committed = append(committed, mutation)
	}
	return nil
}

func codexIdentityMutationConflict(err error) bool {
	return errors.Is(err, errCodexIdentityConflict) && !errors.Is(err, errCodexIdentityRollback)
}

func (h *Handler) applyCodexCredentialIdentityMutation(ctx context.Context, mutation codexCredentialIdentityMutation, rollback bool) error {
	var original, replacement map[string]any
	if errParse := json.Unmarshal(mutation.originalRaw, &original); errParse != nil {
		return errParse
	}
	if errParse := json.Unmarshal(mutation.updatedRaw, &replacement); errParse != nil {
		return errParse
	}
	if rollback {
		original, replacement = replacement, original
	}
	commit := func(auth *coreauth.Auth) error {
		unlock := credentialfile.Lock(mutation.path)
		defer unlock()
		raw, errRead := os.ReadFile(mutation.path)
		if errRead != nil {
			return errRead
		}
		var current map[string]any
		if errParse := json.Unmarshal(raw, &current); errParse != nil {
			return errParse
		}
		if !strings.EqualFold(authMetadataStringValue(current, "type"), "codex") {
			return fmt.Errorf("%w: credential type changed", errCodexIdentityConflict)
		}
		if auth != nil && auth.Storage != nil {
			if _, supported := auth.Storage.(*codex.CodexTokenStorage); !supported {
				return fmt.Errorf("%w: credential storage requires provider synchronization", errCodexIdentityConflict)
			}
		}
		if !codexIdentityFieldsEqual(current, original) {
			// A failed write may have left the original identity untouched.
			if !rollback || !codexIdentityFieldsEqual(current, replacement) {
				return fmt.Errorf("%w: namespace changed concurrently", errCodexIdentityConflict)
			}
		} else {
			copyCodexIdentityFields(current, replacement)
			updatedRaw, errMarshal := json.MarshalIndent(current, "", "  ")
			if errMarshal != nil {
				return errMarshal
			}
			if errWrite := writeCodexCredentialFileAtomic(mutation.path, append(updatedRaw, '\n')); errWrite != nil {
				return errWrite
			}
		}
		if auth != nil {
			if auth.Metadata == nil {
				auth.Metadata = make(map[string]any)
			}
			copyCodexIdentityFields(auth.Metadata, replacement)
			// OAuth may already have persisted fresh credentials while its
			// watcher update is still queued. Publish these canonical fields with
			// the identity so a later routine save cannot restore stale tokens.
			for _, key := range []string{"access_token", "refresh_token", "id_token", "account_id", "email", "expired", "last_refresh", "proxy_url"} {
				if value, exists := current[key]; exists {
					auth.Metadata[key] = value
				} else {
					delete(auth.Metadata, key)
				}
			}
			auth.ProxyURL = authMetadataStringValue(current, "proxy_url")
			if lastRefresh, exists := extractLastRefreshTimestamp(current); exists {
				auth.LastRefreshedAt = lastRefresh
			}
			// File-synthesized Codex auths use metadata persistence. Discard the
			// login-only typed storage, whose omitted fields may contain old tokens.
			auth.Storage = nil
		}
		return nil
	}
	if h.authManager == nil {
		return commit(nil)
	}
	_, errUpdate := h.authManager.UpdatePersistedMetadata(ctx, mutation.auth.ID, commit)
	return errUpdate
}

func copyCodexIdentityFields(target, source map[string]any) {
	for _, key := range []string{codex.CredentialIdentityVersionMetadataKey, codex.CredentialIdentityNamespaceMetadataKey} {
		if value, exists := source[key]; exists {
			target[key] = value
		} else {
			delete(target, key)
		}
	}
}

func codexIdentityFieldsEqual(left, right map[string]any) bool {
	leftIdentity, rightIdentity := make(map[string]any), make(map[string]any)
	copyCodexIdentityFields(leftIdentity, left)
	copyCodexIdentityFields(rightIdentity, right)
	leftRaw, _ := json.Marshal(leftIdentity)
	rightRaw, _ := json.Marshal(rightIdentity)
	return string(leftRaw) == string(rightRaw)
}

func (h *Handler) codexCredentialIdentityPath(auth *coreauth.Auth) string {
	if h == nil || auth == nil || coreauth.IsPluginVirtualAuth(auth) {
		return ""
	}
	if source := auth.AuthSourceKind(); source != "" && source != coreauth.AuthSourceFile {
		return ""
	}
	h.mu.Lock()
	authDirValue := ""
	if h.cfg != nil {
		authDirValue = strings.TrimSpace(h.cfg.AuthDir)
	}
	h.mu.Unlock()
	if authDirValue == "" {
		return ""
	}
	if resolved, errResolve := util.ResolveAuthDir(authDirValue); errResolve == nil && strings.TrimSpace(resolved) != "" {
		authDirValue = resolved
	}
	path := strings.TrimSpace(authAttribute(auth, coreauth.AttributePath))
	if path == "" {
		path = strings.TrimSpace(authAttribute(auth, coreauth.AttributeSource))
	}
	if path == "" && strings.TrimSpace(auth.FileName) != "" {
		path = filepath.Join(authDirValue, auth.FileName)
	}
	if path == "" {
		return ""
	}
	absPath, errAbs := filepath.Abs(path)
	if errAbs != nil {
		return ""
	}
	authDir, errAuthDir := filepath.Abs(authDirValue)
	if errAuthDir != nil || authDir == "" {
		return ""
	}
	relative, errRel := filepath.Rel(authDir, absPath)
	if errRel != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	info, errLstat := os.Lstat(absPath)
	if errLstat != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	return absPath
}

func writeCodexCredentialFileAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	temporary, errCreate := os.CreateTemp(dir, ".codex-identity-*.tmp")
	if errCreate != nil {
		return errCreate
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if errChmod := temporary.Chmod(0o600); errChmod != nil {
		cleanup()
		return errChmod
	}
	if _, errWrite := temporary.Write(raw); errWrite != nil {
		cleanup()
		return errWrite
	}
	if errSync := temporary.Sync(); errSync != nil {
		cleanup()
		return errSync
	}
	if errClose := temporary.Close(); errClose != nil {
		_ = os.Remove(temporaryPath)
		return errClose
	}
	if errRename := os.Rename(temporaryPath, path); errRename != nil {
		_ = os.Remove(temporaryPath)
		return errRename
	}
	directory, errOpenDir := os.Open(dir)
	if errOpenDir != nil {
		return errOpenDir
	}
	errSyncDir := directory.Sync()
	errCloseDir := directory.Close()
	if errSyncDir != nil && !errors.Is(errSyncDir, os.ErrInvalid) {
		return errSyncDir
	}
	if errCloseDir != nil {
		return errCloseDir
	}
	return nil
}

func (h *Handler) disableCodexCredentialIdentityAfterMigrationFailure(ctx context.Context) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.cfg == nil || !h.cfg.Codex.CredentialIdentity.Enabled {
		h.mu.Unlock()
		return
	}
	h.cfg.Codex.CredentialIdentity.Enabled = false
	if strings.TrimSpace(h.configFilePath) == "" {
		h.mu.Unlock()
		return
	}
	if errSave := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); errSave != nil {
		h.mu.Unlock()
		return
	}
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.reloadConfigAfterManagementSave(ctx, snapshot)
}

func codexCredentialIdentityHash(namespace string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(namespace)))
	return hex.EncodeToString(digest[:])[:12]
}

func codexCredentialProxyMode(raw string) string {
	setting, errParse := proxyutil.Parse(raw)
	if errParse != nil || setting.Mode == proxyutil.ModeInvalid {
		return "invalid"
	}
	switch setting.Mode {
	case proxyutil.ModeDirect:
		return "direct"
	case proxyutil.ModeProxy:
		return "proxy"
	default:
		return "inherit"
	}
}

func authMetadataStringValue(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
