package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestInitializeCodexCredentialIdentityMigratesExistingFiles(t *testing.T) {
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	fileNames := []string{"codex-a.json", "codex-b.json"}
	for index, fileName := range fileNames {
		path := filepath.Join(authDir, fileName)
		writeCodexIdentityTestFile(t, path, map[string]any{
			"type":          "codex",
			"email":         "user-" + string(rune('a'+index)) + "@example.com",
			"access_token":  "secret-access-" + fileName,
			"refresh_token": "secret-refresh-" + fileName,
			"proxy_url":     "socks5://127.0.0.1:108" + string(rune('0'+index)),
		})
		registerCodexIdentityTestAuth(t, manager, authDir, fileName)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	response := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(response)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-credential-identity/initialize", strings.NewReader(`{}`))
	h.InitializeCodexCredentialIdentity(ginContext)
	if response.Code != http.StatusOK {
		t.Fatalf("InitializeCodexCredentialIdentity() status = %d body=%s", response.Code, response.Body.String())
	}

	namespaces := make(map[string]struct{})
	for _, fileName := range fileNames {
		path := filepath.Join(authDir, fileName)
		metadata := readCodexIdentityTestFile(t, path)
		if got := metadata["access_token"]; got != "secret-access-"+fileName {
			t.Fatalf("%s access token changed to %v", fileName, got)
		}
		namespace, version, errParse := codex.ParseCredentialIdentity(metadata)
		if errParse != nil {
			t.Fatalf("%s identity invalid: %v", fileName, errParse)
		}
		if version != codex.CredentialIdentityCurrentVersion {
			t.Fatalf("%s version = %d", fileName, version)
		}
		if _, duplicate := namespaces[namespace.String()]; duplicate {
			t.Fatalf("duplicate namespace %s", namespace)
		}
		namespaces[namespace.String()] = struct{}{}
		info, errStat := os.Stat(path)
		if errStat != nil {
			t.Fatal(errStat)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %o, want 600", fileName, got)
		}
	}

	status, errStatus := h.codexCredentialIdentityStatus(context.Background())
	if errStatus != nil {
		t.Fatal(errStatus)
	}
	if !status.AllReady || status.Ready != 2 || status.Missing != 0 {
		t.Fatalf("status after migration = %+v", status)
	}

	// A second run is idempotent and does not rotate namespaces.
	before := readCodexIdentityTestFile(t, filepath.Join(authDir, fileNames[0]))[codex.CredentialIdentityNamespaceMetadataKey]
	response = httptest.NewRecorder()
	ginContext, _ = gin.CreateTestContext(response)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-credential-identity/initialize", strings.NewReader(`{}`))
	h.InitializeCodexCredentialIdentity(ginContext)
	after := readCodexIdentityTestFile(t, filepath.Join(authDir, fileNames[0]))[codex.CredentialIdentityNamespaceMetadataKey]
	if response.Code != http.StatusOK || after != before {
		t.Fatalf("idempotent initialize status=%d before=%v after=%v", response.Code, before, after)
	}

	// A fresh token store sees the same namespace after a simulated restart.
	restartStore := sdkAuth.NewFileTokenStore()
	restartStore.SetBaseDir(authDir)
	reloaded, errList := restartStore.List(context.Background())
	if errList != nil {
		t.Fatal(errList)
	}
	var reloadedNamespace any
	for _, auth := range reloaded {
		if auth != nil && auth.ID == fileNames[0] {
			reloadedNamespace = auth.Metadata[codex.CredentialIdentityNamespaceMetadataKey]
			break
		}
	}
	if reloadedNamespace != before {
		t.Fatalf("namespace after restart = %v, want %v", reloadedNamespace, before)
	}
}

func TestInitializeCodexCredentialIdentityRejectsDuplicateNamespaces(t *testing.T) {
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	shared := uuid.NewString()
	for _, fileName := range []string{"codex-a.json", "codex-b.json"} {
		path := filepath.Join(authDir, fileName)
		writeCodexIdentityTestFile(t, path, map[string]any{
			"type":         "codex",
			"access_token": "secret",
			codex.CredentialIdentityVersionMetadataKey:   1,
			codex.CredentialIdentityNamespaceMetadataKey: shared,
		})
		registerCodexIdentityTestAuth(t, manager, authDir, fileName)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	response := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(response)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-credential-identity/initialize", strings.NewReader(`{}`))
	h.InitializeCodexCredentialIdentity(ginContext)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "share one identity namespace") {
		t.Fatalf("duplicate initialize status=%d body=%s", response.Code, response.Body.String())
	}
	status, errStatus := h.codexCredentialIdentityStatus(context.Background())
	if errStatus != nil {
		t.Fatal(errStatus)
	}
	if status.Conflicts != 2 || status.AllReady {
		t.Fatalf("duplicate status = %+v", status)
	}
}

func TestRotateCodexCredentialIdentityRequiresConfirmationAndChangesOnlyTarget(t *testing.T) {
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	initial := map[string]string{}
	for _, fileName := range []string{"codex-a.json", "codex-b.json"} {
		namespace := uuid.NewString()
		initial[fileName] = namespace
		writeCodexIdentityTestFile(t, filepath.Join(authDir, fileName), map[string]any{
			"type":         "codex",
			"access_token": "secret",
			codex.CredentialIdentityVersionMetadataKey:   1,
			codex.CredentialIdentityNamespaceMetadataKey: namespace,
		})
		registerCodexIdentityTestAuth(t, manager, authDir, fileName)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	response := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(response)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-credential-identity/rotate", strings.NewReader(`{"name":"codex-a.json","confirm":"no"}`))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	h.RotateCodexCredentialIdentity(ginContext)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed rotate status=%d", response.Code)
	}

	response = httptest.NewRecorder()
	ginContext, _ = gin.CreateTestContext(response)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-credential-identity/rotate", strings.NewReader(`{"name":"codex-a.json","confirm":"ROTATE"}`))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	h.RotateCodexCredentialIdentity(ginContext)
	if response.Code != http.StatusOK {
		t.Fatalf("rotate status=%d body=%s", response.Code, response.Body.String())
	}
	afterA := readCodexIdentityTestFile(t, filepath.Join(authDir, "codex-a.json"))[codex.CredentialIdentityNamespaceMetadataKey]
	afterB := readCodexIdentityTestFile(t, filepath.Join(authDir, "codex-b.json"))[codex.CredentialIdentityNamespaceMetadataKey]
	if afterA == initial["codex-a.json"] {
		t.Fatal("target namespace did not rotate")
	}
	if afterB != initial["codex-b.json"] {
		t.Fatal("non-target namespace changed")
	}
}

func TestCodexCredentialIdentityStatusNeverExposesNamespaceOrTokens(t *testing.T) {
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	namespace := uuid.NewString()
	fileName := "codex-private.json"
	writeCodexIdentityTestFile(t, filepath.Join(authDir, fileName), map[string]any{
		"type":         "codex",
		"email":        "private@example.com",
		"access_token": "top-secret-token",
		codex.CredentialIdentityVersionMetadataKey:   1,
		codex.CredentialIdentityNamespaceMetadataKey: namespace,
	})
	registerCodexIdentityTestAuth(t, manager, authDir, fileName)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	response := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(response)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-credential-identity", nil)
	h.GetCodexCredentialIdentity(ginContext)
	if response.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, namespace) || strings.Contains(body, "top-secret-token") {
		t.Fatalf("status response leaked sensitive identity data: %s", body)
	}
	if !strings.Contains(body, codexCredentialIdentityHash(namespace)) {
		t.Fatalf("status response missing short hash: %s", body)
	}
}

func TestCodexCredentialIdentityBatchFailureRollsBackCommittedFiles(t *testing.T) {
	authDir := t.TempDir()
	nestedDir := filepath.Join(authDir, "nested")
	if errMkdir := os.MkdirAll(nestedDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	firstName := "codex-a.json"
	secondName := filepath.Join("nested", "codex-b.json")
	for _, fileName := range []string{firstName, secondName} {
		path := filepath.Join(authDir, fileName)
		writeCodexIdentityTestFile(t, path, map[string]any{"type": "codex", "access_token": "secret"})
		registerCodexIdentityTestAuthPath(t, manager, fileName, path)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	auths, errList := h.codexOAuthCredentials(context.Background())
	if errList != nil {
		t.Fatal(errList)
	}
	mutations, errPlan := h.planCodexCredentialIdentityInitialization(auths)
	if errPlan != nil || len(mutations) != 2 {
		t.Fatalf("plan error=%v mutations=%d", errPlan, len(mutations))
	}
	if errRemove := os.RemoveAll(nestedDir); errRemove != nil {
		t.Fatal(errRemove)
	}
	if errApply := h.applyCodexCredentialIdentityMutations(context.Background(), mutations); errApply == nil {
		t.Fatal("batch mutation error = nil, want failure")
	}
	first := readCodexIdentityTestFile(t, filepath.Join(authDir, firstName))
	if _, exists := first[codex.CredentialIdentityNamespaceMetadataKey]; exists {
		t.Fatalf("first credential was not rolled back: %v", first)
	}
}

func TestPutCodexCredentialIdentityRequiresReadyCredentialsAndPersists(t *testing.T) {
	authDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 8317\nauth-dir: "+authDir+"\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	fileName := "codex-ready.json"
	path := filepath.Join(authDir, fileName)
	writeCodexIdentityTestFile(t, path, map[string]any{"type": "codex", "access_token": "secret"})
	registerCodexIdentityTestAuth(t, manager, authDir, fileName)
	h := NewHandler(&config.Config{AuthDir: authDir}, configPath, manager)

	response := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(response)
	ginContext.Request = httptest.NewRequest(http.MethodPut, "/v0/management/codex-credential-identity", strings.NewReader(`{"enabled":true}`))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	h.PutCodexCredentialIdentity(ginContext)
	if response.Code != http.StatusConflict {
		t.Fatalf("enable before migration status=%d body=%s", response.Code, response.Body.String())
	}

	mutations, errPlan := h.planCodexCredentialIdentityInitialization(manager.List())
	if errPlan != nil {
		t.Fatal(errPlan)
	}
	if errApply := h.applyCodexCredentialIdentityMutations(context.Background(), mutations); errApply != nil {
		t.Fatal(errApply)
	}
	response = httptest.NewRecorder()
	ginContext, _ = gin.CreateTestContext(response)
	ginContext.Request = httptest.NewRequest(http.MethodPut, "/v0/management/codex-credential-identity", strings.NewReader(`{"enabled":true,"credential_proxy_policy":"require"}`))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	h.PutCodexCredentialIdentity(ginContext)
	if response.Code != http.StatusOK {
		t.Fatalf("enable after migration status=%d body=%s", response.Code, response.Body.String())
	}
	loaded, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if !loaded.Codex.CredentialIdentity.Enabled || loaded.Codex.CredentialProxyPolicy != config.CodexCredentialProxyPolicyRequire {
		t.Fatalf("persisted Codex config = %+v", loaded.Codex)
	}
}

func writeCodexIdentityTestFile(t *testing.T, path string, metadata map[string]any) {
	t.Helper()
	raw, errMarshal := json.Marshal(metadata)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o644); errWrite != nil {
		t.Fatal(errWrite)
	}
}

func readCodexIdentityTestFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return metadata
}

func registerCodexIdentityTestAuth(t *testing.T, manager *coreauth.Manager, authDir string, fileName string) {
	t.Helper()
	path := filepath.Join(authDir, fileName)
	registerCodexIdentityTestAuthPath(t, manager, fileName, path)
}

func registerCodexIdentityTestAuthPath(t *testing.T, manager *coreauth.Manager, fileName string, path string) {
	t.Helper()
	metadata := readCodexIdentityTestFile(t, path)
	_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Metadata: metadata,
		Attributes: map[string]string{
			coreauth.AttributePath:          path,
			coreauth.AttributeSource:        path,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
}
