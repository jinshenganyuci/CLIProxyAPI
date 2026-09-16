package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type partialMetadataStorage struct {
	fail bool
}

func (s *partialMetadataStorage) SaveTokenToFile(path string) error {
	previous, errRead := os.ReadFile(path)
	if errRead != nil || string(previous) != `{"token":"original"}` {
		return fmt.Errorf("provider did not receive the original identity snapshot")
	}
	if errWrite := os.WriteFile(path, []byte(`{"token":"`), 0o600); errWrite != nil {
		return errWrite
	}
	if s.fail {
		return fmt.Errorf("synthetic mid-write failure")
	}
	return os.WriteFile(path, []byte(`{"token":"original","concurrency_limit":3}`), 0o600)
}

func TestAtomicMetadataStorageKeepsOriginalOnProviderMidWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential.json")
	if errWrite := os.WriteFile(path, []byte(`{"token":"original"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errSave := WrapAtomicMetadataStorage(&partialMetadataStorage{fail: true}).SaveTokenToFile(path); errSave == nil {
		t.Fatal("expected provider mid-write failure")
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil || string(data) != `{"token":"original"}` {
		t.Fatal("provider failure damaged the original credential")
	}
	entries, errList := os.ReadDir(dir)
	if errList != nil || len(entries) != 1 {
		t.Fatal("provider failure retained staged credential material")
	}
	if errSave := WrapAtomicMetadataStorage(&partialMetadataStorage{}).SaveTokenToFile(path); errSave != nil {
		t.Fatal(errSave)
	}
	data, errRead = os.ReadFile(path)
	if errRead != nil || string(data) != `{"token":"original","concurrency_limit":3}` {
		t.Fatal("successful provider write did not atomically replace the credential")
	}
}
