package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialconcurrency"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type authFileConcurrencyPatch struct {
	Set   bool
	Value *int
}

func decodeAuthFileConcurrencyPatch(fields map[string]json.RawMessage) (authFileConcurrencyPatch, error) {
	for field := range fields {
		root := rootAuthFileField(field)
		if root == "concurrency_in_flight" || root == "concurrency_managed" {
			return authFileConcurrencyPatch{}, fmt.Errorf("%s is read-only", root)
		}
		if root == credentialconcurrency.Field && field != root {
			return authFileConcurrencyPatch{}, fmt.Errorf("concurrency_limit does not support nested fields")
		}
	}
	raw, found := fields[credentialconcurrency.Field]
	if !found {
		return authFileConcurrencyPatch{}, nil
	}
	value, errDecode := decodeAuthFileFieldValue(raw)
	if errDecode != nil {
		return authFileConcurrencyPatch{}, fmt.Errorf("concurrency_limit must be an integer or null")
	}
	if value == nil {
		return authFileConcurrencyPatch{Set: true}, nil
	}
	if _, okNumber := value.(json.Number); !okNumber {
		return authFileConcurrencyPatch{}, fmt.Errorf("concurrency_limit must be an integer or null")
	}
	limit, errParse := credentialconcurrency.ParseValue(value)
	if errParse != nil {
		return authFileConcurrencyPatch{}, errParse
	}
	return authFileConcurrencyPatch{Set: true, Value: &limit}, nil
}

func applyAuthFileConcurrencyPatch(auth *coreauth.Auth, limit *int) {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if limit == nil {
		delete(auth.Metadata, credentialconcurrency.Field)
		delete(auth.Attributes, credentialconcurrency.Field)
		return
	}
	auth.Metadata[credentialconcurrency.Field] = *limit
	auth.Attributes[credentialconcurrency.Field] = strconv.Itoa(*limit)
}

// GetAuthFilesConcurrency exposes only local credential admission counters.
// It avoids disk I/O, provider requests, and plugin callbacks during UI polling.
func (h *Handler) GetAuthFilesConcurrency(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	nameFilter := strings.TrimSpace(c.Query("name"))
	indexFilter := strings.TrimSpace(c.Query("auth_index"))
	entries := make([]gin.H, 0)
	for _, auth := range h.authManager.List() {
		if auth == nil || !matchesAuthFileLookup(auth, nameFilter, indexFilter) {
			continue
		}
		configCredential := auth.AuthSourceKind() == coreauth.AuthSourceConfig
		runtimeOnly := isRuntimeOnlyAuth(auth) || configCredential
		if runtimeOnly && !configCredential && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
			continue
		}
		if !runtimeOnly && strings.TrimSpace(authAttribute(auth, "path")) == "" {
			continue
		}
		name := strings.TrimSpace(auth.FileName)
		if name == "" {
			name = auth.ID
		}
		entry := gin.H{"id": auth.ID, "name": name, "auth_index": lockedAuthIndex(auth)}
		h.addAuthConcurrencyFields(entry, auth)
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i]["id"].(string) < entries[j]["id"].(string)
	})
	c.JSON(http.StatusOK, gin.H{"observed_at": time.Now().UTC(), "entries": entries})
}

func (h *Handler) addAuthConcurrencyFields(entry gin.H, auth *coreauth.Auth) {
	managed := h != nil && h.authManager != nil && h.authManager.HomeEnabled()
	entry["concurrency_managed"] = managed
	entry["concurrency_in_flight"] = nil
	entry[credentialconcurrency.Field] = nil
	if managed {
		return
	}
	entry[credentialconcurrency.Field] = coreauth.EffectiveConcurrencyLimit(auth)
	if h != nil && h.authManager != nil && auth != nil {
		inFlight, limit := h.authManager.CredentialConcurrencySnapshot(auth.ID)
		entry["concurrency_in_flight"] = inFlight
		entry[credentialconcurrency.Field] = limit
	}
}

func (h *Handler) patchConfigAuthConcurrency(c *gin.Context, auth *coreauth.Auth, limit *int) {
	h.mu.Lock()
	original := h.cfg
	candidate := original.CloneForRuntime()
	handled, errSet := setConfigAPIKeyConcurrencyLimit(candidate, auth, limit)
	if errSet != nil || !handled {
		h.mu.Unlock()
		if errSet != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errSet.Error()})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "config api key entry not found"})
		}
		return
	}
	h.cfg = candidate
	snapshot, saved := h.saveConfigAndSnapshotLocked(c)
	if !saved {
		h.cfg = original
	}
	h.mu.Unlock()
	if !saved {
		return
	}
	h.reloadConfigAfterManagementSave(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "via": "config:concurrency-limit"})
}
