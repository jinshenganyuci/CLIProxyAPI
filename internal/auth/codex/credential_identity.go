package codex

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	// CredentialIdentityVersionMetadataKey is the persisted schema version key.
	CredentialIdentityVersionMetadataKey = "codex_identity_version"
	// CredentialIdentityNamespaceMetadataKey is the persisted UUID namespace key.
	CredentialIdentityNamespaceMetadataKey = "codex_identity_namespace"
	// CredentialIdentityCurrentVersion is the currently supported mapping schema.
	CredentialIdentityCurrentVersion = 1
	// CredentialIdentityConflictAttribute marks a runtime auth whose namespace is
	// duplicated by another Codex OAuth credential in the same instance.
	CredentialIdentityConflictAttribute = "codex_identity_conflict"
)

const credentialIdentityDerivationPrefix = "cpa:codex:credential-identity:v1\x00"

// NewCredentialIdentityNamespace creates a persistent namespace for one OAuth credential.
func NewCredentialIdentityNamespace() string {
	return uuid.NewString()
}

// ParseCredentialIdentity reads and validates a credential identity from auth metadata.
func ParseCredentialIdentity(metadata map[string]any) (uuid.UUID, int, error) {
	if metadata == nil {
		return uuid.Nil, 0, fmt.Errorf("codex credential identity metadata is missing")
	}

	version, errVersion := credentialIdentityVersion(metadata[CredentialIdentityVersionMetadataKey])
	if errVersion != nil {
		return uuid.Nil, 0, errVersion
	}
	if version != CredentialIdentityCurrentVersion {
		return uuid.Nil, version, fmt.Errorf("unsupported codex credential identity version %d", version)
	}

	rawNamespace, ok := metadata[CredentialIdentityNamespaceMetadataKey].(string)
	if !ok || strings.TrimSpace(rawNamespace) == "" {
		return uuid.Nil, version, fmt.Errorf("codex credential identity namespace is missing")
	}
	namespace, errParse := uuid.Parse(strings.TrimSpace(rawNamespace))
	if errParse != nil || namespace == uuid.Nil {
		return uuid.Nil, version, fmt.Errorf("codex credential identity namespace is invalid")
	}
	return namespace, version, nil
}

// SetCredentialIdentity stores a validated namespace and the current schema version.
func SetCredentialIdentity(metadata map[string]any, namespace string) error {
	if metadata == nil {
		return fmt.Errorf("codex credential identity metadata is nil")
	}
	parsed, errParse := uuid.Parse(strings.TrimSpace(namespace))
	if errParse != nil || parsed == uuid.Nil {
		return fmt.Errorf("codex credential identity namespace is invalid")
	}
	metadata[CredentialIdentityVersionMetadataKey] = CredentialIdentityCurrentVersion
	metadata[CredentialIdentityNamespaceMetadataKey] = parsed.String()
	return nil
}

// EnsureCredentialIdentity preserves a valid existing namespace or initializes one.
func EnsureCredentialIdentity(metadata map[string]any) (string, bool, error) {
	if metadata == nil {
		return "", false, fmt.Errorf("codex credential identity metadata is nil")
	}
	if _, hasVersion := metadata[CredentialIdentityVersionMetadataKey]; hasVersion {
		namespace, _, errParse := ParseCredentialIdentity(metadata)
		if errParse != nil {
			return "", false, errParse
		}
		return namespace.String(), false, nil
	}
	if _, hasNamespace := metadata[CredentialIdentityNamespaceMetadataKey]; hasNamespace {
		return "", false, fmt.Errorf("codex credential identity version is missing")
	}

	namespace := NewCredentialIdentityNamespace()
	if errSet := SetCredentialIdentity(metadata, namespace); errSet != nil {
		return "", false, errSet
	}
	return namespace, true, nil
}

// DeriveCredentialIdentity maps a client identifier into one credential namespace.
func DeriveCredentialIdentity(namespace uuid.UUID, kind string, value string) string {
	kind = strings.TrimSpace(kind)
	value = strings.TrimSpace(value)
	if namespace == uuid.Nil || kind == "" || value == "" {
		return value
	}
	name := credentialIdentityDerivationPrefix + kind + "\x00" + value
	return uuid.NewSHA1(namespace, []byte(name)).String()
}

func credentialIdentityVersion(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if math.Trunc(typed) != typed {
			return 0, fmt.Errorf("codex credential identity version is invalid")
		}
		parsed := int(typed)
		if float64(parsed) != typed {
			return 0, fmt.Errorf("codex credential identity version is invalid")
		}
		return parsed, nil
	case json.Number:
		parsed, errParse := strconv.Atoi(string(typed))
		if errParse != nil {
			return 0, fmt.Errorf("codex credential identity version is invalid")
		}
		return parsed, nil
	case string:
		parsed, errParse := strconv.Atoi(strings.TrimSpace(typed))
		if errParse != nil {
			return 0, fmt.Errorf("codex credential identity version is invalid")
		}
		return parsed, nil
	case nil:
		return 0, fmt.Errorf("codex credential identity version is missing")
	default:
		return 0, fmt.Errorf("codex credential identity version is invalid")
	}
}
