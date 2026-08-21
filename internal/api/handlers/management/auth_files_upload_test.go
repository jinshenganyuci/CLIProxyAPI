package management

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestUploadAuthFile_PreservesPriorityAttributes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	content := `{"type":"codex","email":"midai0530@gmail.com","priority":98}`

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "codex-midai0530@gmail.com-plus.json")
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err = part.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write multipart content: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if status, _ := payload["status"].(string); status != "ok" {
		t.Fatalf("expected status ok, got %#v", payload["status"])
	}

	auth, ok := manager.GetByID("codex-midai0530@gmail.com-plus.json")
	if !ok || auth == nil {
		t.Fatalf("expected uploaded auth record to exist")
	}
	if got := auth.Attributes["priority"]; got != "98" {
		t.Fatalf("priority attribute = %q, want %q", got, "98")
	}
	if got := auth.Metadata["priority"]; got != float64(98) {
		t.Fatalf("priority metadata = %#v, want 98", got)
	}
}

func TestWriteAuthFileAddsAndPreservesCodexCredentialIdentity(t *testing.T) {
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	fileName := "codex-upload.json"

	if errWrite := h.writeAuthFile(t.Context(), fileName, []byte(`{"type":"codex","email":"user@example.com","access_token":"first-token","refresh_token":"first-refresh"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	first := readCodexIdentityTestFile(t, filepath.Join(authDir, fileName))
	namespace, _, errIdentity := codex.ParseCredentialIdentity(first)
	if errIdentity != nil {
		t.Fatalf("uploaded credential identity invalid: %v", errIdentity)
	}
	info, errStat := os.Stat(filepath.Join(authDir, fileName))
	if errStat != nil {
		t.Fatal(errStat)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("uploaded credential mode = %o, want 600", info.Mode().Perm())
	}

	if errWrite := h.writeAuthFile(t.Context(), fileName, []byte(`{"type":"codex","email":"user@example.com","access_token":"second-token","refresh_token":"second-refresh"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	second := readCodexIdentityTestFile(t, filepath.Join(authDir, fileName))
	secondNamespace, _, errIdentity := codex.ParseCredentialIdentity(second)
	if errIdentity != nil {
		t.Fatal(errIdentity)
	}
	if secondNamespace != namespace {
		t.Fatalf("overwrite rotated namespace from %s to %s", namespace, secondNamespace)
	}
	if second["access_token"] != "second-token" {
		t.Fatalf("OAuth token did not update: %v", second["access_token"])
	}
}

func TestWriteAuthFileRejectsDuplicateCodexCredentialIdentity(t *testing.T) {
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	sharedNamespace := "170d9fd8-4779-4a6d-9a01-7566e82318f9"
	payload := func(email string) []byte {
		return []byte(`{"type":"codex","email":"` + email + `","access_token":"secret","codex_identity_version":1,"codex_identity_namespace":"` + sharedNamespace + `"}`)
	}
	if errWrite := h.writeAuthFile(t.Context(), "codex-a.json", payload("a@example.com")); errWrite != nil {
		t.Fatal(errWrite)
	}
	errWrite := h.writeAuthFile(t.Context(), "codex-b.json", payload("b@example.com"))
	if errWrite == nil || !strings.Contains(errWrite.Error(), "conflicts") {
		t.Fatalf("duplicate namespace upload error = %v", errWrite)
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "codex-b.json")); !os.IsNotExist(errStat) {
		t.Fatalf("conflicting upload created file, stat error = %v", errStat)
	}
}
