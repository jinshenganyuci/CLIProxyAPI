package codex

import (
	"fmt"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

// CloneForMetadataUpdate preserves native credential fields in independent storage.
func (s *CodexTokenStorage) CloneForMetadataUpdate() (baseauth.TokenStorage, error) {
	if s == nil {
		return nil, fmt.Errorf("codex token storage is nil")
	}
	cloned := *s
	var errClone error
	cloned.Metadata, errClone = baseauth.CloneMetadataForUpdate(s.Metadata)
	if errClone != nil {
		return nil, errClone
	}
	return &cloned, nil
}
