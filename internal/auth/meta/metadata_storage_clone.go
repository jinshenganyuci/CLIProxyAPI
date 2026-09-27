package meta

import (
	"fmt"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

// CloneForMetadataUpdate preserves native tokens and isolates mutable metadata.
func (s *MetaTokenStorage) CloneForMetadataUpdate() (baseauth.TokenStorage, error) {
	cloned := CloneTokenStorage(s)
	if cloned == nil {
		return nil, fmt.Errorf("cannot clone Meta token storage")
	}
	return cloned, nil
}

// CloneForMetadataUpdate drops the completed login's guard and frozen controls.
func (s *oauthTokenStorage) CloneForMetadataUpdate() (baseauth.TokenStorage, error) {
	cloned := CloneTokenStorage(s)
	if cloned == nil {
		return nil, fmt.Errorf("cannot clone Meta OAuth token storage")
	}
	return cloned, nil
}
