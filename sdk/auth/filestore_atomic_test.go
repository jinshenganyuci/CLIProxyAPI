package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteAuthMetadataAtomicDoesNotLeavePartialFileOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "credential.json")
	if errMkdir := os.Mkdir(target, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	marker := filepath.Join(target, "previous")
	if errWrite := os.WriteFile(marker, []byte("untouched"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errWrite := writeAuthMetadataAtomic(target, []byte(`{"concurrency_limit":3}`)); errWrite == nil {
		t.Fatal("expected rename over a nonempty directory to fail")
	}
	data, errRead := os.ReadFile(marker)
	if errRead != nil || string(data) != "untouched" {
		t.Fatal("failed atomic replacement changed the previous target")
	}
	entries, errRead := os.ReadDir(dir)
	if errRead != nil || len(entries) != 1 {
		t.Fatalf("temporary credential data was not cleaned: entries=%d error=%v", len(entries), errRead)
	}
}

func TestWriteAuthMetadataAtomicReplacesCompleteContentsWithPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.json")
	if errWrite := os.WriteFile(path, []byte("old contents much longer than new"), 0o644); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errWrite := writeAuthMetadataAtomic(path, []byte(`{"concurrency_limit":3}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil || string(data) != `{"concurrency_limit":3}` {
		t.Fatal("replacement did not contain the complete new credential")
	}
	info, errStat := os.Stat(path)
	if errStat != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("credential mode is not private: %v", errStat)
	}
}
