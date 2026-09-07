package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func newCodexIdentityTransactionTestHandler(t *testing.T, initialized bool) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transaction.json")
	metadata := map[string]any{"type": "codex", "access_token": "old-access", "refresh_token": "old-refresh"}
	if initialized {
		if errSet := codex.SetCredentialIdentity(metadata, codex.NewCredentialIdentityNamespace()); errSet != nil {
			t.Fatal(errSet)
		}
	}
	writeCodexIdentityTestFile(t, path, metadata)
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	registerCodexIdentityTestAuth(t, manager, dir, filepath.Base(path))
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager), path
}

func refreshCodexIdentityTransactionTestAuth(t *testing.T, h *Handler, path string) {
	t.Helper()
	base, _ := h.authManager.GetByID(filepath.Base(path))
	refreshed := base.Clone()
	refreshed.Metadata["access_token"] = "new-access"
	refreshed.Metadata["refresh_token"] = "new-refresh"
	refreshed.ProxyURL = "socks5://127.0.0.1:1089"
	refreshed.Metadata["proxy_url"] = refreshed.ProxyURL
	if _, errRefresh := h.authManager.UpdateRefreshedAuth(context.Background(), base, refreshed); errRefresh != nil {
		t.Fatal(errRefresh)
	}
}

func assertCodexIdentityTransactionTestRefresh(t *testing.T, h *Handler, path string) {
	t.Helper()
	runtimeAuth, _ := h.authManager.GetByID(filepath.Base(path))
	for label, metadata := range map[string]map[string]any{"disk": readCodexIdentityTestFile(t, path), "runtime": runtimeAuth.Metadata} {
		if metadata["access_token"] != "new-access" || metadata["refresh_token"] != "new-refresh" {
			t.Errorf("%s lost the concurrent token refresh", label)
		}
		if metadata["proxy_url"] != "socks5://127.0.0.1:1089" {
			t.Errorf("%s lost the concurrent proxy update", label)
		}
	}
	if runtimeAuth.ProxyURL != "socks5://127.0.0.1:1089" {
		t.Error("runtime proxy field lost the concurrent update")
	}
}

func TestCodexCredentialIdentityTransactionPreservesConcurrentRefresh(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		name := "initialize"
		if initialized {
			name = "rotate"
		}
		t.Run(name, func(t *testing.T) {
			h, path := newCodexIdentityTransactionTestHandler(t, initialized)
			base, _ := h.authManager.GetByID(filepath.Base(path))
			mutation, errPlan := h.planCodexCredentialIdentityMutation(base, codex.NewCredentialIdentityNamespace(), initialized)
			if errPlan != nil {
				t.Fatal(errPlan)
			}
			refreshCodexIdentityTransactionTestAuth(t, h, path)
			if errApply := h.applyCodexCredentialIdentityMutations(context.Background(), []codexCredentialIdentityMutation{mutation}); errApply != nil {
				t.Fatal(errApply)
			}
			assertCodexIdentityTransactionTestRefresh(t, h, path)
		})
	}
}

func TestCodexCredentialIdentityRollbackPreservesConcurrentRefresh(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		h, path := newCodexIdentityTransactionTestHandler(t, initialized)
		base, _ := h.authManager.GetByID(filepath.Base(path))
		mutation, errPlan := h.planCodexCredentialIdentityMutation(base, codex.NewCredentialIdentityNamespace(), initialized)
		if errPlan != nil {
			t.Fatal(errPlan)
		}
		if errApply := h.applyCodexCredentialIdentityMutation(context.Background(), mutation, false); errApply != nil {
			t.Fatal(errApply)
		}
		refreshCodexIdentityTransactionTestAuth(t, h, path)
		if errRollback := h.applyCodexCredentialIdentityMutation(context.Background(), mutation, true); errRollback != nil {
			t.Fatal(errRollback)
		}
		assertCodexIdentityTransactionTestRefresh(t, h, path)
		if !codexIdentityFieldsEqual(readCodexIdentityTestFile(t, path), base.Metadata) {
			t.Fatal("rollback did not restore only the original identity")
		}
	}
}

func TestCodexCredentialIdentityRollbackRejectsNewerIdentity(t *testing.T) {
	h, path := newCodexIdentityTransactionTestHandler(t, true)
	base, _ := h.authManager.GetByID(filepath.Base(path))
	first, errPlan := h.planCodexCredentialIdentityMutation(base, codex.NewCredentialIdentityNamespace(), true)
	if errPlan != nil {
		t.Fatal(errPlan)
	}
	if errApply := h.applyCodexCredentialIdentityMutation(context.Background(), first, false); errApply != nil {
		t.Fatal(errApply)
	}
	base, _ = h.authManager.GetByID(filepath.Base(path))
	second, errPlan := h.planCodexCredentialIdentityMutation(base, codex.NewCredentialIdentityNamespace(), true)
	if errPlan != nil {
		t.Fatal(errPlan)
	}
	if errApply := h.applyCodexCredentialIdentityMutation(context.Background(), second, false); errApply != nil {
		t.Fatal(errApply)
	}
	if errRollback := h.applyCodexCredentialIdentityMutation(context.Background(), first, true); errRollback == nil {
		t.Fatal("rollback accepted a namespace changed by another transaction")
	}
	if got := readCodexIdentityTestFile(t, path)[codex.CredentialIdentityNamespaceMetadataKey]; got != second.namespace {
		t.Fatal("rollback replaced the newer identity")
	}
}

func TestCodexCredentialIdentitySynchronizesOAuthSaveBeforeWatcher(t *testing.T) {
	for _, typedStorage := range []bool{false, true} {
		t.Run(fmt.Sprintf("typed_storage_%t", typedStorage), func(t *testing.T) {
			h, path := newCodexIdentityTransactionTestHandler(t, true)
			h.cfg.Codex.CredentialIdentity.Enabled = true
			base, _ := h.authManager.GetByID(filepath.Base(path))
			base.Metadata["id_token"] = "old-id"
			base.Metadata["plugin_owned"] = "keep-this-value"
			base.Attributes["plugin_owned"] = "keep-this-attribute"
			runtimeMarker := &struct{}{}
			base.Runtime = runtimeMarker
			if typedStorage {
				base.Storage = &codex.CodexTokenStorage{IDToken: "typed-old-id", AccessToken: "typed-old-access", RefreshToken: "typed-old-refresh"}
			}
			if _, errUpdate := h.authManager.Update(coreauth.WithSkipPersist(context.Background()), base); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			fresh := base.Clone()
			fresh.Storage = nil
			fresh.Metadata["access_token"] = "oauth-new-access"
			fresh.Metadata["refresh_token"] = "oauth-new-refresh"
			fresh.Metadata["account_id"] = "oauth-new-account"
			fresh.Metadata["proxy_url"] = "socks5://127.0.0.1:1099"
			delete(fresh.Metadata, "id_token")
			store := sdkAuth.NewFileTokenStore()
			store.SetBaseDir(filepath.Dir(path))
			if _, errSave := store.Save(context.Background(), fresh); errSave != nil {
				t.Fatal(errSave)
			}
			// The OAuth file has been saved; its watcher update has not run yet.
			response := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(response)
			ginContext.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-credential-identity/rotate", strings.NewReader(`{"name":"transaction.json","confirm":"ROTATE"}`))
			h.RotateCodexCredentialIdentity(ginContext)
			if response.Code != http.StatusOK {
				t.Fatalf("rotation status=%d body=%s", response.Code, response.Body.String())
			}
			current, _ := h.authManager.GetByID(base.ID)
			if current.Storage != nil || current.Metadata["plugin_owned"] != "keep-this-value" || current.Attributes["plugin_owned"] != "keep-this-attribute" || current.Runtime != runtimeMarker {
				t.Fatal("canonical credential synchronization damaged plugin state or retained typed storage")
			}
			if current.ProxyURL != "socks5://127.0.0.1:1099" {
				t.Fatal("runtime proxy did not follow the canonical OAuth file")
			}
			current.Metadata["note"] = "ordinary-metadata-update"
			if _, errUpdate := h.authManager.Update(context.Background(), current); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			for label, metadata := range map[string]map[string]any{"runtime": current.Metadata, "disk_after_ordinary_save": readCodexIdentityTestFile(t, path)} {
				if metadata["access_token"] != "oauth-new-access" || metadata["refresh_token"] != "oauth-new-refresh" || metadata["account_id"] != "oauth-new-account" || metadata["proxy_url"] != "socks5://127.0.0.1:1099" {
					t.Fatalf("%s reverted a canonical credential field", label)
				}
				if _, exists := metadata["id_token"]; exists {
					t.Fatalf("%s resurrected the removed ID token", label)
				}
			}
		})
	}
}

type unsupportedCodexIdentityStorage struct{}

func (*unsupportedCodexIdentityStorage) SaveTokenToFile(string) error {
	return fmt.Errorf("unsupported storage must not be invoked by identity mutation")
}

func TestCodexCredentialIdentityConflictPreservesEnabledConfig(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		h, path := newCodexIdentityTransactionTestHandler(t, initialized)
		h.cfg.Codex.CredentialIdentity.Enabled = true
		base, _ := h.authManager.GetByID(filepath.Base(path))
		base.Storage = &unsupportedCodexIdentityStorage{}
		if _, errUpdate := h.authManager.Update(coreauth.WithSkipPersist(context.Background()), base); errUpdate != nil {
			t.Fatal(errUpdate)
		}
		before := readCodexIdentityTestFile(t, path)
		response := httptest.NewRecorder()
		ginContext, _ := gin.CreateTestContext(response)
		if initialized {
			ginContext.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-credential-identity/rotate", strings.NewReader(`{"name":"transaction.json","confirm":"ROTATE"}`))
			h.RotateCodexCredentialIdentity(ginContext)
		} else {
			ginContext.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-credential-identity/initialize", strings.NewReader(`{}`))
			h.InitializeCodexCredentialIdentity(ginContext)
		}
		if response.Code != http.StatusConflict || !h.cfg.Codex.CredentialIdentity.Enabled {
			t.Fatalf("safe conflict changed the enabled feature: code=%d enabled=%t body=%s", response.Code, h.cfg.Codex.CredentialIdentity.Enabled, response.Body.String())
		}
		if !codexIdentityFieldsEqual(before, readCodexIdentityTestFile(t, path)) {
			t.Fatal("safe conflict modified the credential identity")
		}
	}
}
