package meta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/credentialfile"
)

// ErrOAuthProxyMismatch prevents implicit relogin from replacing a selected account route.
var ErrOAuthProxyMismatch = errors.New("existing Meta credential uses a different proxy; select its proxy explicitly before signing in again")

type oauthTokenStorage struct {
	metadata     map[string]any
	proxyURL     string
	explicit     bool
	acquiredKeys map[string]bool
	credentials  map[string]any
	merged       map[string]json.RawMessage
	metadataSets int
	beforeSave   func() error
}

func (s *oauthTokenStorage) SetMetadata(metadata map[string]any) {
	if s.metadataSets == 0 {
		// The first injection is the generic metadata merge when a file exists;
		// a later injection includes post-auth hook changes and the final save.
		if raw, errMarshal := json.Marshal(metadata); errMarshal == nil {
			_ = json.Unmarshal(raw, &s.merged)
		}
	}
	s.metadataSets++
	s.metadata = metadata
}

func (s *oauthTokenStorage) SaveTokenToFile(path string) error {
	if errDir := os.MkdirAll(filepath.Dir(path), 0o700); errDir != nil {
		return fmt.Errorf("create Meta credential directory: %w", errDir)
	}
	unlock := credentialfile.Lock(path)
	defer unlock()
	if s.beforeSave != nil {
		if errGuard := s.beforeSave(); errGuard != nil {
			return errGuard
		}
	}
	// Inspect the latest file under the same lock used for the replacement.
	data, errRead := os.ReadFile(path)
	if errRead != nil && !errors.Is(errRead, os.ErrNotExist) {
		return fmt.Errorf("read existing Meta credential: %w", errRead)
	}
	latest := make(map[string]any)
	if errRead == nil {
		if errDecode := json.Unmarshal(data, &latest); errDecode != nil || latest == nil {
			return fmt.Errorf("existing Meta credential is invalid")
		}
	}
	if !s.explicit {
		if previous, _ := latest["proxy_url"].(string); strings.TrimSpace(previous) != "" {
			previousRoute, errPrevious := ResolveOAuthProxy(previous)
			if errPrevious != nil || previousRoute != s.proxyURL {
				return ErrOAuthProxyMismatch
			}
		}
	}
	if s.metadata == nil {
		return fmt.Errorf("Meta credential metadata is unavailable")
	}
	// Start with current canonical controls and overlay new token/profile fields
	// and actual hook edits. Unchanged fields from an earlier metadata merge must
	// not undo concurrent control changes or resurrect concurrently deleted keys.
	for key, value := range s.metadata {
		if _, managed := metaCredentialFields[key]; managed {
			continue
		}
		if s.acquiredKeys[key] {
			latest[key] = value
			continue
		}
		if s.metadataSets > 1 {
			before, existed := s.merged[key]
			encoded, errEncode := json.Marshal(value)
			if errEncode != nil {
				return fmt.Errorf("encode Meta credential field: %w", errEncode)
			}
			if !existed || !bytes.Equal(before, encoded) {
				latest[key] = value
			}
		} else if _, exists := latest[key]; !exists {
			latest[key] = value
		}
	}
	if s.metadataSets > 1 {
		for key := range s.merged {
			if _, present := s.metadata[key]; !present {
				delete(latest, key)
			}
		}
	}
	// The native storage owns credential lifecycle fields. Generic metadata merges
	// may contain old tokens or expiry values that the new grant intentionally omits.
	for key := range metaCredentialFields {
		delete(latest, key)
	}
	for key, value := range s.credentials {
		latest[key] = value
	}
	latest["proxy_url"] = s.proxyURL
	raw, errMarshal := json.Marshal(latest)
	if errMarshal != nil {
		return fmt.Errorf("encode Meta credential: %w", errMarshal)
	}
	temporary, errCreate := os.CreateTemp(filepath.Dir(path), ".meta-oauth-token-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create Meta credential temporary file: %w", errCreate)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, errWrite := temporary.Write(raw); errWrite != nil {
		_ = temporary.Close()
		return fmt.Errorf("write Meta credential: %w", errWrite)
	}
	if errSync := temporary.Sync(); errSync != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync Meta credential: %w", errSync)
	}
	if errClose := temporary.Close(); errClose != nil {
		return fmt.Errorf("close Meta credential: %w", errClose)
	}
	if s.beforeSave != nil {
		if errGuard := s.beforeSave(); errGuard != nil {
			return errGuard
		}
	}
	if errRename := os.Rename(temporaryPath, path); errRename != nil {
		return fmt.Errorf("replace Meta credential: %w", errRename)
	}
	for key := range s.metadata {
		delete(s.metadata, key)
	}
	for key, value := range latest {
		s.metadata[key] = value
	}
	return nil
}
