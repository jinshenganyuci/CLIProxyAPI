package vertex

import (
	"fmt"

	baseauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth"
)

// CloneForMetadataUpdate preserves native credential fields in independent storage.
func (s *VertexCredentialStorage) CloneForMetadataUpdate() (baseauth.TokenStorage, error) {
	if s == nil {
		return nil, fmt.Errorf("vertex token storage is nil")
	}
	cloned := *s
	var errClone error
	cloned.Metadata, errClone = baseauth.CloneMetadataForUpdate(s.Metadata)
	if errClone != nil {
		return nil, errClone
	}
	cloned.ServiceAccount, errClone = baseauth.CloneMetadataForUpdate(s.ServiceAccount)
	if errClone != nil {
		return nil, errClone
	}
	return &cloned, nil
}
