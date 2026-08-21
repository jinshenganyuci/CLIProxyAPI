package watcher

import (
	"strings"

	internalcodex "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func markCodexCredentialIdentityConflicts(auths []*coreauth.Auth) {
	owners := make(map[string][]*coreauth.Auth)
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if auth.Attributes != nil {
			delete(auth.Attributes, internalcodex.CredentialIdentityConflictAttribute)
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.AuthKind() != coreauth.AuthKindOAuth {
			continue
		}
		namespace, _, errIdentity := internalcodex.ParseCredentialIdentity(auth.Metadata)
		if errIdentity != nil {
			continue
		}
		owners[namespace.String()] = append(owners[namespace.String()], auth)
	}
	for _, credentials := range owners {
		if len(credentials) < 2 {
			continue
		}
		for _, auth := range credentials {
			if auth.Attributes == nil {
				auth.Attributes = make(map[string]string)
			}
			auth.Attributes[internalcodex.CredentialIdentityConflictAttribute] = "true"
		}
	}
}
