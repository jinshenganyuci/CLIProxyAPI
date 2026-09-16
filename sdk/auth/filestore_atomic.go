package auth

import (
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

// writeAuthMetadataAtomic keeps the previous credential intact until all bytes
// have been written and synchronized. Rename is the final, atomic commit point.
func writeAuthMetadataAtomic(path string, data []byte) error {
	file, errCreate := os.CreateTemp(filepath.Dir(path), ".auth-metadata-*.tmp")
	if errCreate != nil {
		return errCreate
	}
	temporary := file.Name()
	closed, committed := false, false
	defer func() {
		if !closed {
			if errClose := file.Close(); errClose != nil {
				log.WithError(errClose).Warn("failed to close temporary credential metadata")
			}
		}
		if !committed {
			if errRemove := os.Remove(temporary); errRemove != nil && !os.IsNotExist(errRemove) {
				log.WithError(errRemove).Warn("failed to remove temporary credential metadata")
			}
		}
	}()
	if _, errWrite := file.Write(data); errWrite != nil {
		return errWrite
	}
	if errSync := file.Sync(); errSync != nil {
		return errSync
	}
	errClose := file.Close()
	closed = true
	if errClose != nil {
		return errClose
	}
	if errRename := os.Rename(temporary, path); errRename != nil {
		return errRename
	}
	committed = true
	return nil
}
