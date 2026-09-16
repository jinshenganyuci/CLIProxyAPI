package claude

import (
	"fmt"

	baseauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth"
)

// CloneForMetadataUpdate preserves native credential fields in independent storage.
func (s *ClaudeTokenStorage) CloneForMetadataUpdate() (baseauth.TokenStorage, error) {
	if s == nil {
		return nil, fmt.Errorf("claude token storage is nil")
	}
	cloned := *s
	var errClone error
	cloned.Metadata, errClone = baseauth.CloneMetadataForUpdate(s.Metadata)
	if errClone != nil {
		return nil, errClone
	}
	cloned.DeviceIDs = append([]string(nil), s.DeviceIDs...)
	return &cloned, nil
}
