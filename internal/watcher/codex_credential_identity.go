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

// reconcileCodexCredentialIdentityConflictsLocked updates every affected owner,
// including credentials outside the changed file. Call before stamping revisions
// so stale scans and delayed dispatch cannot restore an obsolete conflict marker.
func (w *Watcher) reconcileCodexCredentialIdentityConflictsLocked(updates []AuthUpdate) []AuthUpdate {
	auths := make([]*coreauth.Auth, 0, len(w.currentAuths))
	for _, auth := range w.currentAuths {
		if auth != nil {
			auths = append(auths, auth.Clone())
		}
	}
	markCodexCredentialIdentityConflicts(auths)
	updatedIDs := make(map[string]bool, len(updates))
	for _, update := range updates {
		id := update.ID
		if id == "" && update.Auth != nil {
			id = update.Auth.ID
		}
		updatedIDs[id] = true
	}
	for _, auth := range auths {
		existing := w.currentAuths[auth.ID]
		if existing.Attributes[internalcodex.CredentialIdentityConflictAttribute] == auth.Attributes[internalcodex.CredentialIdentityConflictAttribute] {
			continue
		}
		w.currentAuths[auth.ID] = auth
		if !updatedIDs[auth.ID] {
			updates = append(updates, AuthUpdate{
				Action: AuthUpdateActionModify, ID: auth.ID, Auth: auth.Clone(),
				identityConflictOnly: true,
			})
		}
	}
	for index := range updates {
		update := &updates[index]
		if update.Auth != nil && update.Action != AuthUpdateActionDelete {
			if current := w.currentAuths[update.Auth.ID]; current != nil {
				update.Auth = current.Clone()
			}
		}
	}
	return updates
}
