package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
)

// StripOpaqueReasoningRouterSuffix removes a validated routing tail from a
// compatible reasoning value that the caller has already chosen to preserve.
// The inspector checks only the transport shape; this does not identify the
// ciphertext as Grok, grant cross-provider compatibility, or validate its key.
// Unsuffixed and unrecognized opaque values remain byte-for-byte unchanged.
func StripOpaqueReasoningRouterSuffix(raw string) string {
	if !strings.Contains(raw, ".") {
		return raw
	}
	info, errInspect := signature.InspectGrokEncryptedContent(raw)
	if errInspect != nil || info.RouterSuffix == "" {
		return raw
	}
	return raw[:len(raw)-len(info.RouterSuffix)-1]
}
