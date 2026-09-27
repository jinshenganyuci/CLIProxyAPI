package management

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func rejectInvalidCredentialConcurrencyLimit(c *gin.Context, field string, limit *int) bool {
	if errValidate := config.ValidateCredentialConcurrencyLimit(limit); errValidate != nil {
		c.JSON(400, gin.H{"error": fmt.Sprintf("%s: %v", field, errValidate)})
		return true
	}
	return false
}

// applyCredentialConcurrencyLimitPatch preserves omitted values and resets null to the default.
func applyCredentialConcurrencyLimitPatch(c *gin.Context, raw json.RawMessage, target **int) bool {
	if len(raw) == 0 {
		return true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*target = nil
		return true
	}
	var limit int
	if errUnmarshal := json.Unmarshal(raw, &limit); errUnmarshal != nil {
		c.JSON(400, gin.H{"error": "concurrency-limit must be an integer"})
		return false
	}
	if rejectInvalidCredentialConcurrencyLimit(c, "concurrency-limit", &limit) {
		return false
	}
	*target = &limit
	return true
}
