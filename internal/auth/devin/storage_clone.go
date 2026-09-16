package devin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	baseauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/credentialfile"
)

// metadataTokenStorage retains Devin's complete credential payload after login.
// It has no acquisition guard or frozen route that can override operator edits.
type metadataTokenStorage struct {
	metadata map[string]any
}

func (s *oauthTokenStorage) CloneForMetadataUpdate() (baseauth.TokenStorage, error) {
	if s == nil {
		return nil, fmt.Errorf("Devin OAuth token storage is nil")
	}
	metadata, errClone := baseauth.CloneMetadataForUpdate(s.metadata)
	if errClone != nil {
		return nil, errClone
	}
	return &metadataTokenStorage{metadata: metadata}, nil
}

func (s *metadataTokenStorage) CloneForMetadataUpdate() (baseauth.TokenStorage, error) {
	if s == nil {
		return nil, fmt.Errorf("Devin token storage is nil")
	}
	metadata, errClone := baseauth.CloneMetadataForUpdate(s.metadata)
	if errClone != nil {
		return nil, errClone
	}
	return &metadataTokenStorage{metadata: metadata}, nil
}

func (s *metadataTokenStorage) SetMetadata(metadata map[string]any) {
	s.metadata = metadata
}

func (s *metadataTokenStorage) SaveTokenToFile(path string) error {
	if s == nil || s.metadata == nil {
		return fmt.Errorf("Devin credential metadata is unavailable")
	}
	raw, errEncode := json.Marshal(s.metadata)
	if errEncode != nil {
		return fmt.Errorf("encode Devin credential: %w", errEncode)
	}
	if errDir := os.MkdirAll(filepath.Dir(path), 0o700); errDir != nil {
		return fmt.Errorf("create Devin credential directory: %w", errDir)
	}
	unlock := credentialfile.Lock(path)
	defer unlock()
	temporary, errCreate := os.CreateTemp(filepath.Dir(path), ".devin-metadata-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create Devin credential temporary file: %w", errCreate)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, errWrite := temporary.Write(raw); errWrite != nil {
		_ = temporary.Close()
		return fmt.Errorf("write Devin credential: %w", errWrite)
	}
	if errSync := temporary.Sync(); errSync != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync Devin credential: %w", errSync)
	}
	if errClose := temporary.Close(); errClose != nil {
		return fmt.Errorf("close Devin credential: %w", errClose)
	}
	if errRename := os.Rename(temporaryPath, path); errRename != nil {
		return fmt.Errorf("replace Devin credential: %w", errRename)
	}
	return nil
}
