package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// CodexWebsocketConnectionKey binds a retained connection to the proxy and
// credential identity used for its handshake without exposing proxy credentials.
func CodexWebsocketConnectionKey(cfg *config.Config, auth *cliproxyauth.Auth) string {
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if setting, errParse := proxyutil.Parse(proxyURL); errParse == nil && setting.Mode == proxyutil.ModeDirect {
		proxyURL = "direct"
	}
	namespace, version := "", ""
	if cfg != nil && cfg.Codex.CredentialIdentity.Enabled && auth != nil {
		namespace, _ = auth.Metadata["codex_identity_namespace"].(string)
		version = fmt.Sprint(auth.Metadata["codex_identity_version"])
	}
	if proxyURL == "" && namespace == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(proxyURL + "\x00" + namespace + "\x00" + version))
	return hex.EncodeToString(digest[:])
}
