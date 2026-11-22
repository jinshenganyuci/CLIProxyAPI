package store

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestUpgrade830ObjectConfigSeedHTTPAndRestart(t *testing.T) {
	var mu sync.Mutex
	var remote []byte
	puts, gets := 0, 0
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	objectStore := objectConfigTestStore(t, configPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/bucket/"+objectStoreConfigKey {
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPut:
			if got := r.Header.Get("If-None-Match"); got != "*" {
				t.Errorf("If-None-Match = %q, want literal *", got)
			}
			data, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				http.Error(w, "read failed", http.StatusBadRequest)
				return
			}
			remote = bytes.Clone(data)
			puts++
			w.Header().Set("ETag", `"830seed"`)
		case http.MethodHead, http.MethodGet:
			if remote == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", `"830seed"`)
			w.Header().Set("Last-Modified", "Wed, 01 Jan 2025 00:00:00 GMT")
			w.Header().Set("Content-Type", "application/x-yaml")
			w.Header().Set("Content-Length", strconv.Itoa(len(remote)))
			if r.Method == http.MethodGet {
				gets++
				_, _ = w.Write(remote)
			}
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	})
	const activeSecret = "upgrade830-active-fixture"
	const obsoleteSecret = "upgrade830-obsolete-fixture"
	writeSeedTestFile(t, configPath, fmt.Sprintf("# operator note\nport: 8317\nremote-management:\n  secret-key: %s\nmanagement:\n  secret-key: %s\n", obsoleteSecret, activeSecret))
	created, errSync := objectStore.syncConfigFromBucket(t.Context(), "")
	if errSync != nil {
		t.Fatal(errSync)
	}
	mu.Lock()
	seeded := bytes.Clone(remote)
	mu.Unlock()
	if bytes.Contains(seeded, []byte(activeSecret)) || bytes.Contains(seeded, []byte(obsoleteSecret)) || !bytes.Contains(seeded, []byte("# operator note")) {
		t.Fatal("uploaded seed retained plaintext or lost the operator comment")
	}
	loaded, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if errCompare := bcrypt.CompareHashAndPassword([]byte(loaded.RemoteManagement.SecretKey), []byte(activeSecret)); errCompare != nil {
		t.Fatal("seeded secret does not match the effective v8 management secret")
	}
	if loaded.SourceHash != config.ComputeSourceHash(seeded) || !bytes.Equal(readSeedTestFile(t, configPath), seeded) {
		t.Fatal("startup load changed the uploaded seed or its source fingerprint")
	}
	if created.ServerID == "" || loaded.ServerID != created.ServerID {
		t.Fatal("startup load did not reuse the server ID in the uploaded seed")
	}
	writeSeedTestFile(t, configPath, "port: 9999\n")
	restarted, errRestart := objectStore.syncConfigFromBucket(t.Context(), "")
	if errRestart != nil {
		t.Fatal(errRestart)
	}
	if restarted.ServerID != created.ServerID {
		t.Fatal("restart did not reuse the server ID from object storage")
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 1 || gets != 1 || !bytes.Equal(readSeedTestFile(t, configPath), seeded) || !bytes.Equal(remote, seeded) {
		t.Fatalf("restart did not fetch the exact seed without another upload: puts=%d gets=%d", puts, gets)
	}
}
