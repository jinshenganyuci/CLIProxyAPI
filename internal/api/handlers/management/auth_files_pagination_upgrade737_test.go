package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestUpgrade737AuthFilesPaginationPreservesCredentialKinds(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	for _, home := range []bool{false, true} {
		t.Run(fmt.Sprintf("home-%t", home), func(t *testing.T) {
			limit := 3
			cfg := &config.Config{AuthDir: t.TempDir(), CodexKey: []config.CodexKey{
				{APIKey: "synthetic-config-active", BaseURL: "https://active.invalid", ConcurrencyLimit: &limit},
				{APIKey: "synthetic-config-disabled", BaseURL: "https://disabled.invalid"},
			}}
			cfg.Home.Enabled = home
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			configAuths, errSynthesize := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
				Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator(),
			})
			if errSynthesize != nil || len(configAuths) != 2 {
				t.Fatalf("config synthesis: %v; count=%d", errSynthesize, len(configAuths))
			}
			configAuths[1].Disabled = true
			configAuths[1].Status = coreauth.StatusDisabled
			expected := map[string]bool{}
			for _, auth := range configAuths {
				registerAuthForLookupTest(t, manager, auth)
				expected[auth.ID] = true
			}
			path := filepath.Join(cfg.AuthDir, "file.json")
			if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			auths := []*coreauth.Auth{
				{ID: "file", FileName: "file.json", Attributes: map[string]string{"path": path}, Metadata: map[string]any{"concurrency_limit": 3}},
				{ID: "virtual", FileName: "file.json", Attributes: map[string]string{"path": path}},
				{ID: "runtime", Attributes: map[string]string{"runtime_only": "true"}},
				{ID: "disabled-file", FileName: "disabled-file.json", Disabled: true, Attributes: map[string]string{"path": path}},
				{ID: "missing-active", FileName: "missing-active.json", Attributes: map[string]string{"path": filepath.Join(cfg.AuthDir, "missing.json")}},
				{ID: "hidden-runtime", Disabled: true, Attributes: map[string]string{"runtime_only": "true"}},
				{ID: "hidden-removed", Disabled: true, Attributes: map[string]string{"path": filepath.Join(cfg.AuthDir, "removed.json")}},
				{ID: "hidden-pathless"},
			}
			coreauth.MarkPluginVirtualAuth(auths[1], path, 0)
			for i, auth := range auths {
				auth.Provider = "codex"
				auth.Status = coreauth.StatusActive
				if auth.Disabled {
					auth.Status = coreauth.StatusDisabled
				}
				registerAuthForLookupTest(t, manager, auth)
				if i < 5 {
					expected[auth.ID] = true
				}
			}
			if !home {
				for range 2 {
					release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), auths[0])
					if errAcquire != nil {
						t.Fatal(errAcquire)
					}
					t.Cleanup(release)
				}
			}
			handler := NewHandlerWithoutConfigFilePath(cfg, manager)
			unpaginated := requestAuthFilesPage(t, handler, "/auth-files")
			full := make(map[string]map[string]any)
			for _, entry := range unpaginated.Files {
				id := entry["id"].(string)
				if !expected[id] {
					t.Fatalf("unexpected unpaginated credential %q", id)
				}
				full[id] = entry
			}
			if len(full) != len(expected) {
				t.Fatalf("unpaginated total=%d, want %d", len(full), len(expected))
			}
			seen := map[string]bool{}
			for page := 1; page <= 4; page++ {
				result := requestAuthFilesPage(t, handler, fmt.Sprintf("/auth-files?page=%d&page_size=2", page))
				if result.Total != len(expected) || result.HasMore != (page < 4) {
					t.Errorf("page %d total=%d has_more=%t, want %d/%t", page, result.Total, result.HasMore, len(expected), page < 4)
				}
				for _, entry := range result.Files {
					id := entry["id"].(string)
					if seen[id] || full[id] == nil {
						t.Fatalf("duplicate or unexpected paginated credential %q", id)
					}
					seen[id] = true
					for _, field := range []string{"id", "auth_index", "name", "runtime_only", "disabled", "concurrency_config", "concurrency_limit", "concurrency_in_flight", "concurrency_managed", "cooldowns"} {
						if !reflect.DeepEqual(entry[field], full[id][field]) {
							t.Errorf("credential %s field %s = %#v; full list has %#v", id, field, entry[field], full[id][field])
						}
					}
					if entry["auth_index"] == "" || entry["concurrency_managed"] != home {
						t.Errorf("credential identity/Home marker incorrect for %s", id)
					}
					if home && (entry["concurrency_limit"] != nil || entry["concurrency_in_flight"] != nil || entry["cooldowns"] != nil) {
						t.Errorf("Home credential exposed local authoritative counters: %s", id)
					}
				}
			}
			if !reflect.DeepEqual(seen, expected) {
				t.Errorf("paged credential set=%v, want %v", seen, expected)
			}
			for _, auth := range configAuths {
				filtered := requestAuthFilesPage(t, handler, "/auth-files?page=1&page_size=1&auth_index="+url.QueryEscape(full[auth.ID]["auth_index"].(string)))
				if filtered.Total != 1 || len(filtered.Files) != 1 || filtered.Files[0]["id"] != auth.ID {
					t.Errorf("config credential index filter lost credential %s: %#v", auth.ID, filtered)
				}
			}
			shared := requestAuthFilesPage(t, handler, "/auth-files?page=1&page_size=1&name=file.json")
			if shared.Total != 2 || len(shared.Files) != 1 || !shared.HasMore {
				t.Errorf("file/virtual shared name total=%d files=%d", shared.Total, len(shared.Files))
			}
			if !home {
				if full["file"]["concurrency_in_flight"] != float64(2) || full["file"]["concurrency_limit"] != float64(3) || full["runtime"]["concurrency_limit"] != float64(10) {
					t.Errorf("local live/default concurrency changed: file=%v runtime=%v", full["file"], full["runtime"])
				}
			}
		})
	}
}

func TestUpgrade737AuthFilesPaginationBoundsAndQueryCompatibility(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk-%t", disk), func(t *testing.T) {
			authDir := t.TempDir()
			var manager *coreauth.Manager
			if !disk {
				manager = coreauth.NewManager(nil, nil, nil)
			}
			registerPaginatedAuthFiles(t, authDir, manager)
			handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
			maxInt := int(^uint(0) >> 1)
			for _, query := range []string{
				fmt.Sprintf("?page=%d&page_size=%d", maxInt, maxInt),
				fmt.Sprintf("?page=%d&page_size=1", maxInt),
				"?page=2&page_size=5",
			} {
				result := requestAuthFilesPage(t, handler, "/auth-files"+query)
				if result.Total != 5 || len(result.Files) != 0 || result.HasMore {
					t.Errorf("out-of-range %s: %#v", query, result)
				}
			}
			largePage := requestAuthFilesPage(t, handler, fmt.Sprintf("/auth-files?page_size=%d", maxInt))
			if largePage.Total != 5 || len(largePage.Files) != 5 || largePage.HasMore {
				t.Errorf("large page: %#v", largePage)
			}
			for _, query := range []string{"?page=-1", "?page_size=-1", "?page=1.5", "?page_size=1e3", "?page=99999999999999999999999999", "?page=%20", "?page_size=%20", "?page=&page_size="} {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodGet, "/auth-files"+query, nil)
				handler.ListAuthFiles(ctx)
				if recorder.Code != http.StatusBadRequest {
					t.Errorf("invalid %s status=%d", query, recorder.Code)
				}
			}
			for _, query := range []string{"?prefix=../", "?prefix=missing"} {
				result := requestAuthFilesPage(t, handler, "/auth-files"+query)
				if len(result.Files) != 5 || result.Page != 0 || result.PageSize != 0 {
					t.Errorf("legacy unknown query %s: %#v", query, result)
				}
			}
			missing := requestAuthFilesPage(t, handler, "/auth-files?page=1&name=..%2Foutside.json")
			if missing.Total != 0 || len(missing.Files) != 0 || missing.HasMore {
				t.Errorf("name filter was not an exact match: %#v", missing)
			}
		})
	}
}

func TestUpgrade737AuthFilesPaginationLargeStableOrdering(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	manager := coreauth.NewManager(nil, nil, nil)
	const total = 1001
	for i := total - 1; i >= 0; i-- {
		name := fmt.Sprintf("name-%04d.json", i/2)
		if i%2 == 0 {
			name = strings.ToUpper(name)
		}
		registerAuthForLookupTest(t, manager, &coreauth.Auth{
			ID: fmt.Sprintf("auth-%04d", i), FileName: name, Provider: "codex", Status: coreauth.StatusActive,
			Attributes: map[string]string{"runtime_only": "true"},
		})
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	seen := map[string]bool{}
	lastName := ""
	for page := 1; page <= 15; page++ {
		result := requestAuthFilesPage(t, handler, fmt.Sprintf("/auth-files?page=%d&page_size=71", page))
		if result.Total != total || result.HasMore != (page < 15) {
			t.Fatalf("page %d metadata: total=%d has_more=%t", page, result.Total, result.HasMore)
		}
		repeated := requestAuthFilesPage(t, handler, fmt.Sprintf("/auth-files?page=%d&page_size=71", page))
		if !reflect.DeepEqual(authFileNames(result.Files), authFileNames(repeated.Files)) {
			t.Fatalf("page %d order changed across identical requests", page)
		}
		for _, entry := range result.Files {
			id := entry["id"].(string)
			name := entry["name"].(string)
			if seen[id] || strings.ToLower(name) < strings.ToLower(lastName) {
				t.Fatalf("duplicate/out-of-order credential: %s %s", id, name)
			}
			seen[id] = true
			lastName = name
		}
	}
	if len(seen) != total {
		t.Fatalf("collected %d unique credentials, want %d", len(seen), total)
	}
}
