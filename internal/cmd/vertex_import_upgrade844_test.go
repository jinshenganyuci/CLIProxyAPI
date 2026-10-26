package cmd

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"github.com/tidwall/gjson"
)

func TestUpgrade844VertexCLIImportPersistsGlobalLocation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	raw, err := json.Marshal(map[string]any{"type": "service_account", "project_id": "local-probe", "client_email": "probe@example.invalid", "private_key": string(keyPEM)})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-service-account.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	previous := sdkauth.GetTokenStore()
	sdkauth.RegisterTokenStore(sdkauth.NewFileTokenStore())
	t.Cleanup(func() { sdkauth.RegisterTokenStore(previous) })
	authDir := filepath.Join(dir, "auths")
	DoVertexImport(&config.Config{AuthDir: authDir}, path, "separate")
	saved, err := os.ReadFile(filepath.Join(authDir, "vertex-separate-local-probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"location": "global", "project_id": "local-probe", "prefix": "separate", "type": "vertex"} {
		if gjson.GetBytes(saved, field).String() != want {
			t.Fatalf("persisted %s did not match import metadata", field)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, raw) {
		t.Fatal("import mutated the source service account file")
	}
}
