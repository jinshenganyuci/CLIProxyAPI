package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/credentialfile"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialconcurrency"
)

type codexRefreshBaseContextKey struct{}

// IsCodexAuthRecord reports whether auth represents a Codex credential record.
func IsCodexAuthRecord(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return true
	}
	if auth.Metadata != nil {
		if t, ok := auth.Metadata["type"].(string); ok && strings.EqualFold(strings.TrimSpace(t), "codex") {
			return true
		}
	}
	if _, ok := auth.Storage.(*codex.CodexTokenStorage); ok {
		return true
	}
	return false
}

// PersistCodexAuthFile persists a Codex auth record to path using inter-process file locking,
// read-before-write merging, and scoped refresh patches when present.
// It returns handled=false when auth is not a Codex credential record.
func PersistCodexAuthFile(ctx context.Context, path string, auth *Auth) (bool, error) {
	if !IsCodexAuthRecord(auth) {
		return false, nil
	}

	type metadataSetter interface {
		SetMetadata(map[string]any)
	}
	var refreshBase *Auth
	if ctx != nil {
		refreshBase, _ = ctx.Value(codexRefreshBaseContextKey{}).(*Auth)
	}

	switch {
	case auth.CodexRefreshPatch != nil || refreshBase != nil:
		errRefresh := codex.WithFileLock(path, func() error {
			existing, errRead := os.ReadFile(path)
			if errRead != nil {
				if os.IsNotExist(errRead) {
					if auth.Metadata == nil {
						auth.Metadata = make(map[string]any)
					}
					auth.Metadata["disabled"] = auth.Disabled
					NormalizeCredentialMetadata(auth.Metadata)
					return codex.WriteJSONFileAtomic(path, auth.Metadata)
				}
				return fmt.Errorf("auth filestore: read refresh source failed: %w", errRead)
			}
			var diskMap map[string]any
			if errUnmarshal := json.Unmarshal(existing, &diskMap); errUnmarshal != nil || diskMap == nil {
				return fmt.Errorf("auth filestore: invalid refresh source JSON")
			}
			merged := diskMap
			controlsCurrent := refreshBase != nil
			if auth.CodexRefreshPatch != nil {
				controlsCurrent = controlsCurrent && auth.CodexRefreshPatch.CanApplyTo(diskMap)
				merged = auth.CodexRefreshPatch.Apply(diskMap)
			} else if refreshBase != nil {
				diskAuth := refreshBase.Clone()
				diskAuth.Metadata = diskMap
				controlsCurrent = !CredentialsChanged(refreshBase, diskAuth)
			}
			if controlsCurrent {
				var errControls error
				merged, errControls = mergeCodexRefreshControls(refreshBase, auth, diskMap, merged)
				if errControls != nil {
					return errControls
				}
			}
			NormalizeCredentialMetadata(merged)
			if errPreserve := codex.PreserveFileCredentialIdentity(path, merged); errPreserve != nil {
				return errPreserve
			}
			if errWrite := codex.WriteJSONFileAtomic(path, merged); errWrite != nil {
				return errWrite
			}
			auth.Metadata = merged
			return nil
		})
		if errRefresh != nil {
			return true, errRefresh
		}
		auth.CodexRefreshPatch = nil
		auth.Storage = nil
		if refreshBase != nil {
			auth.ProxyURL, _ = auth.Metadata["proxy_url"].(string)
			delete(auth.Attributes, credentialconcurrency.Field)
			if errLimit := ApplyAuthConcurrencyLimitMetadata(auth, auth.Metadata); errLimit != nil {
				return true, errLimit
			}
			delete(auth.Attributes, AttributeWeight)
			if errWeight := ApplyAuthWeightMetadata(auth, auth.Metadata); errWeight != nil {
				return true, errWeight
			}
			_, hadPriority := auth.Attributes["priority"]
			delete(auth.Attributes, "priority")
			ApplyAuthPriorityMetadata(auth, auth.Metadata)
			// An omitted zero priority is the existing plugin default; retain its
			// representation without dropping explicit file ownership.
			if !hadPriority && auth.Attributes["priority"] == "0" {
				delete(auth.Attributes, "priority")
			}
		}
		if disabled, ok := auth.Metadata["disabled"].(bool); ok || refreshBase != nil {
			auth.Disabled = disabled
			if disabled {
				auth.Status = StatusDisabled
			} else if auth.Status == StatusDisabled {
				auth.Status = StatusActive
			}
		}
		return true, nil

	case auth.Storage != nil:
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["disabled"] = auth.Disabled
		if storage, ok := auth.Storage.(*codex.CodexTokenStorage); ok && storage != nil {
			clonedStorage := *storage
			clonedStorage.ReplaceCredentials = HasAuthCreationIntent(ctx)
			auth.Storage = &clonedStorage
		}
		if setter, ok := auth.Storage.(metadataSetter); ok {
			setter.SetMetadata(auth.Metadata)
		}
		if errSave := auth.Storage.SaveTokenToFile(path); errSave != nil {
			return true, errSave
		}
		if persistedBytes, errReadPersisted := credentialfile.ReadFile(path); errReadPersisted == nil && len(persistedBytes) > 0 {
			var diskMap map[string]any
			if errUnmarshal := json.Unmarshal(persistedBytes, &diskMap); errUnmarshal == nil && len(diskMap) > 0 {
				auth.Metadata = codex.MergeCodexCredentialsFromDisk(diskMap, auth.Metadata)
				preservePersistedCodexIdentity(auth.Metadata, diskMap)
				NormalizeCredentialMetadata(auth.Metadata)
			}
		}
		return true, nil

	case auth.Metadata != nil:
		auth.Metadata["disabled"] = auth.Disabled
		errLock := codex.WithFileLock(path, func() error {
			existing, errRead := os.ReadFile(path)
			if errRead == nil && len(existing) > 0 {
				var diskMap map[string]any
				if errUnmarshal := json.Unmarshal(existing, &diskMap); errUnmarshal == nil && len(diskMap) > 0 {
					if !HasAuthCreationIntent(ctx) {
						auth.Metadata = codex.MergeCodexCredentialsFromDisk(diskMap, auth.Metadata)
					}
					auth.Metadata["disabled"] = auth.Disabled
					NormalizeCredentialMetadata(auth.Metadata)
					if errPreserve := codex.PreserveFileCredentialIdentity(path, auth.Metadata); errPreserve != nil {
						return errPreserve
					}
					if raw, errMarshal := json.Marshal(auth.Metadata); errMarshal == nil && string(existing) == string(raw) {
						return nil
					}
				}
			} else if errRead != nil && !os.IsNotExist(errRead) {
				return fmt.Errorf("auth filestore: read existing failed: %w", errRead)
			}
			if errPreserve := codex.PreserveFileCredentialIdentity(path, auth.Metadata); errPreserve != nil {
				return errPreserve
			}
			return codex.WriteJSONFileAtomic(path, auth.Metadata)
		})
		if errLock != nil {
			return true, errLock
		}
		return true, nil

	default:
		return false, nil
	}
}

// Refresh retains the SDK's control-field three-way merge while native patches
// alone own credential partitions. Compare JSON values in their persisted form
// so an in-memory integer and the same disk number do not look like an edit.
func mergeCodexRefreshControls(base, accepted *Auth, disk, credentials map[string]any) (map[string]any, error) {
	controls := func(source *Auth) (*Auth, error) {
		out := source.Clone()
		raw, errMarshal := json.Marshal(source.Metadata)
		if errMarshal != nil {
			return nil, fmt.Errorf("auth filestore: encode refresh controls: %w", errMarshal)
		}
		if errUnmarshal := json.Unmarshal(raw, &out.Metadata); errUnmarshal != nil {
			return nil, fmt.Errorf("auth filestore: decode refresh controls: %w", errUnmarshal)
		}
		NormalizeCredentialMetadata(out.Metadata)
		for key := range out.Metadata {
			if isCodexRefreshProtectedMetadata(key) {
				delete(out.Metadata, key)
			}
		}
		out.CodexRefreshPatch = nil
		return out, nil
	}
	baseControls, errBase := controls(base)
	if errBase != nil {
		return nil, errBase
	}
	acceptedControls, errAccepted := controls(accepted)
	if errAccepted != nil {
		return nil, errAccepted
	}
	diskAuth := base.Clone()
	diskAuth.Metadata = disk
	diskControls, errDisk := controls(diskAuth)
	if errDisk != nil {
		return nil, errDisk
	}
	if !reflect.DeepEqual(baseControls.Metadata["proxy_url"], diskControls.Metadata["proxy_url"]) {
		diskControls.ProxyURL, _ = diskControls.Metadata["proxy_url"].(string)
	}
	mergedControls := mergeAuthContent(baseControls, diskControls, acceptedControls)
	merged := make(map[string]any, len(credentials)+len(mergedControls.Metadata))
	for key, value := range credentials {
		if isCodexRefreshProtectedMetadata(key) {
			merged[key] = value
		}
	}
	for key, value := range mergedControls.Metadata {
		merged[key] = value
	}
	validation := accepted.Clone()
	validation.Metadata = merged
	delete(validation.Attributes, credentialconcurrency.Field)
	delete(validation.Attributes, AttributeWeight)
	if errLimit := ValidateAuthConcurrencyLimit(validation); errLimit != nil {
		return nil, errLimit
	}
	if errWeight := ValidateAuthWeight(validation); errWeight != nil {
		return nil, errWeight
	}
	return merged, nil
}

func isCodexRefreshProtectedMetadata(key string) bool {
	return codex.IsCodexCredentialKey(key) || IsAuthTokenPayloadKey(key) ||
		key == codex.CredentialIdentityVersionMetadataKey || key == codex.CredentialIdentityNamespaceMetadataKey
}

// A token save can retain the durable identity rather than the incoming storage
// metadata. Publish the same identity back to the manager after that save.
func preservePersistedCodexIdentity(metadata, disk map[string]any) {
	if _, hasVersion := disk[codex.CredentialIdentityVersionMetadataKey]; !hasVersion {
		if _, hasNamespace := disk[codex.CredentialIdentityNamespaceMetadataKey]; !hasNamespace {
			return
		}
	}
	for _, key := range []string{codex.CredentialIdentityVersionMetadataKey, codex.CredentialIdentityNamespaceMetadataKey} {
		if value, exists := disk[key]; exists {
			metadata[key] = value
		} else {
			delete(metadata, key)
		}
	}
}
