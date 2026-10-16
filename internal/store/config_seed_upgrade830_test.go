package store

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestUpgrade830ObjectConfigSeedHTTPAndRestart(t *testing.T) {
	var mu sync.Mutex
	var remote []byte
	puts, gets := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/bucket/"+objectStoreConfigKey {
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPut:
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
	}))
	t.Cleanup(server.Close)
	// Anonymous local S3 requests keep the fixture body unwrapped by AWS chunk signing.
	client, errClient := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("", "", ""), Region: "us-east-1"})
	if errClient != nil {
		t.Fatal(errClient)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	const activeSecret = "upgrade830-active-fixture"
	const obsoleteSecret = "upgrade830-obsolete-fixture"
	writeSeedTestFile(t, configPath, fmt.Sprintf("# operator note\nport: 8317\nremote-management:\n  secret-key: %s\nmanagement:\n  secret-key: %s\n", obsoleteSecret, activeSecret))
	objectStore := &ObjectTokenStore{client: client, cfg: ObjectStoreConfig{Bucket: "bucket"}, configPath: configPath}
	if errSync := objectStore.syncConfigFromBucket(t.Context(), ""); errSync != nil {
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
	writeSeedTestFile(t, configPath, "port: 9999\n")
	if errSync := objectStore.syncConfigFromBucket(t.Context(), ""); errSync != nil {
		t.Fatal(errSync)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 1 || gets != 1 || !bytes.Equal(readSeedTestFile(t, configPath), seeded) || !bytes.Equal(remote, seeded) {
		t.Fatalf("restart did not fetch the exact seed without another upload: puts=%d gets=%d", puts, gets)
	}
}
