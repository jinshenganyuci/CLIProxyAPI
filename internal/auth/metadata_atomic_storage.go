package auth

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/credentialfile"
	log "github.com/sirupsen/logrus"
)

// WrapAtomicMetadataStorage stages metadata edits without changing the runtime
// storage type. Only the copy passed to Store.Save should use this wrapper.
func WrapAtomicMetadataStorage(storage TokenStorage) TokenStorage {
	if storage == nil {
		return nil
	}
	if _, wrapped := storage.(*atomicMetadataStorage); wrapped {
		return storage
	}
	return &atomicMetadataStorage{storage: storage}
}

type atomicMetadataStorage struct {
	storage TokenStorage
}

func (s *atomicMetadataStorage) SetMetadata(metadata map[string]any) {
	if setter, ok := s.storage.(interface{ SetMetadata(map[string]any) }); ok {
		setter.SetMetadata(metadata)
	}
}

func (s *atomicMetadataStorage) SaveTokenToFile(path string) error {
	if s == nil || s.storage == nil {
		return fmt.Errorf("metadata storage is unavailable")
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return errMkdir
	}
	unlock := credentialfile.Lock(path)
	defer unlock()
	previous, errRead := os.ReadFile(path)
	if errRead != nil && !os.IsNotExist(errRead) {
		return errRead
	}
	file, errCreate := os.CreateTemp(filepath.Dir(path), ".auth-control-*.tmp")
	if errCreate != nil {
		return errCreate
	}
	temporary := file.Name()
	defer func() {
		if errRemove := os.Remove(temporary); errRemove != nil && !os.IsNotExist(errRemove) {
			log.WithError(errRemove).Warn("failed to clean staged credential metadata")
		}
	}()
	// Seed the original file so provider-specific identity preservation reads the
	// same previous contents even though it writes to an isolated staging path.
	_, errWrite := file.Write(previous)
	errClose := file.Close()
	if errWrite != nil {
		return errWrite
	}
	if errClose != nil {
		return errClose
	}
	if os.IsNotExist(errRead) {
		if errRemove := os.Remove(temporary); errRemove != nil {
			return errRemove
		}
	}
	if errSave := s.storage.SaveTokenToFile(temporary); errSave != nil {
		return errSave
	}
	if errChmod := os.Chmod(temporary, 0o600); errChmod != nil {
		return errChmod
	}
	staged, errOpen := os.Open(temporary)
	if errOpen != nil {
		return errOpen
	}
	errSync := staged.Sync()
	errClose = staged.Close()
	if errSync != nil {
		return errSync
	}
	if errClose != nil {
		return errClose
	}
	// No fallible operation follows this commit point: success on disk must not
	// subsequently be reported as a failure to the runtime metadata transaction.
	return os.Rename(temporary, path)
}
