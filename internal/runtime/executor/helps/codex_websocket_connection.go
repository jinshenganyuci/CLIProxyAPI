package helps

import (
	"context"
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
func CodexWebsocketConnectionKey(cfg *config.Config, auth *cliproxyauth.Auth, executionProxy ...string) string {
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if len(executionProxy) > 0 {
		proxyURL = executionProxy[0]
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

// CodexWebsocketExecutionConnectionKey validates the request route before checking
// whether a retained credential/namespace connection is eligible for reuse.
func CodexWebsocketExecutionConnectionKey(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (string, error) {
	proxyURL, errProxy := ResolveExecutionProxy(ctx, cfg, auth)
	if errProxy != nil {
		return "", errProxy
	}
	return CodexWebsocketConnectionKey(cfg, auth, proxyURL), nil
}
