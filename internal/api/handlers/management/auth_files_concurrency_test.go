package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func concurrencyManagementRequest(h *Handler, method, target, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	if method == http.MethodPatch {
		h.PatchAuthFileFields(ctx)
	} else {
		h.GetAuthFilesConcurrency(ctx)
	}
	return recorder
}

func newConcurrencyManagementFixture(t *testing.T) (*Handler, *coreauth.Manager, *coreauth.Auth) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID: "a.json", FileName: "a.json", Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"path": filepath.Join(dir, "a.json")},
		Metadata:   map[string]any{"type": "codex", "access_token": "synthetic-private-token", "proxy_url": "http://synthetic:private@proxy.invalid:8080"},
	}
	registered, errRegister := manager.Register(context.Background(), record)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager), manager, registered
}

func TestAuthFileConcurrencyPatchPersistsAndRestoresDefault(t *testing.T) {
	h, manager, record := newConcurrencyManagementFixture(t)
	for _, raw := range []string{"3", "null"} {
		recorder := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", `{"name":"a.json","concurrency_limit":`+raw+`}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("patch %s: status=%d body=%s", raw, recorder.Code, recorder.Body.String())
		}
		want := 3
		if raw == "null" {
			want = 10
		}
		current, _ := manager.GetByID(record.ID)
		if got := coreauth.EffectiveConcurrencyLimit(current); got != want {
			t.Fatalf("runtime limit=%d, want %d", got, want)
		}
		data, errRead := os.ReadFile(filepath.Join(h.cfg.AuthDir, "a.json"))
		if errRead != nil {
			t.Fatal(errRead)
		}
		var metadata map[string]any
		if errDecode := json.Unmarshal(data, &metadata); errDecode != nil {
			t.Fatal(errDecode)
		}
		if got := coreauth.EffectiveConcurrencyLimit(&coreauth.Auth{Metadata: metadata}); got != want {
			t.Fatalf("persisted limit=%d, want %d", got, want)
		}
		if raw == "null" {
			if _, exists := metadata["concurrency_limit"]; exists {
				t.Fatal("default restoration retained an explicit limit")
			}
		}
		if metadata["access_token"] != "synthetic-private-token" || metadata["proxy_url"] != record.Metadata["proxy_url"] {
			t.Fatal("concurrency update changed credential or proxy metadata")
		}
	}
}

func TestAuthFileConcurrencyPatchRejectsInvalidWithoutMutation(t *testing.T) {
	h, manager, record := newConcurrencyManagementFixture(t)
	before, errRead := os.ReadFile(filepath.Join(h.cfg.AuthDir, "a.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, fragment := range []string{
		`"concurrency_limit":0`, `"concurrency_limit":-1`, `"concurrency_limit":1.5`,
		`"concurrency_limit":1000001`, `"concurrency_limit":1e100`, `"concurrency_limit":"3"`,
		`"concurrency_limit":true`, `"concurrency_limit":[]`, `"concurrency_limit":{}`,
		`"concurrency_limit.nested":3`, `"concurrency_limit . nested":3`,
		`"concurrency_in_flight":0`, `"concurrency_managed":false`,
	} {
		t.Run(fragment, func(t *testing.T) {
			recorder := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", `{"name":"a.json","note":"must-not-save",`+fragment+`}`)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			current, _ := manager.GetByID(record.ID)
			if coreauth.EffectiveConcurrencyLimit(current) != 10 || current.Metadata["note"] != nil {
				t.Fatal("rejected patch mutated runtime metadata")
			}
			after, errRead := os.ReadFile(filepath.Join(h.cfg.AuthDir, "a.json"))
			if errRead != nil || string(before) != string(after) {
				t.Fatal("rejected patch changed the stored credential")
			}
		})
	}
}

func TestAuthFileConcurrencySnapshotTracksLiveLeasesAndLimitChanges(t *testing.T) {
	h, manager, record := newConcurrencyManagementFixture(t)
	for range 3 {
		release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), record)
		if errAcquire != nil {
			t.Fatal(errAcquire)
		}
		t.Cleanup(release)
	}
	recorder := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", `{"name":"a.json","concurrency_limit":2}`)
	if recorder.Code != http.StatusOK {
		t.Fatal(recorder.Body.String())
	}
	recorder = concurrencyManagementRequest(h, http.MethodGet, "/auth-files/concurrency?name=a.json", "")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v", recorder.Code, recorder.Header())
	}
	var result struct {
		Entries []map[string]any `json:"entries"`
	}
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &result); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(result.Entries) != 1 || result.Entries[0]["concurrency_in_flight"] != float64(3) || result.Entries[0]["concurrency_limit"] != float64(2) {
		t.Fatalf("snapshot=%s", recorder.Body.String())
	}
	for _, sensitive := range []string{"synthetic-private-token", "proxy.invalid", "access_token", "proxy_url"} {
		if strings.Contains(recorder.Body.String(), sensitive) {
			t.Fatalf("snapshot exposed %q", sensitive)
		}
	}
	if len(result.Entries[0]) != 6 {
		t.Fatalf("unexpected snapshot fields: %v", result.Entries[0])
	}
	if recorder := concurrencyManagementRequest(h, http.MethodGet, "/auth-files/concurrency?auth_index=not-present", ""); !strings.Contains(recorder.Body.String(), `"entries":[]`) {
		t.Fatalf("mismatched index was not filtered: %s", recorder.Body.String())
	}
	current, _ := manager.GetByID(record.ID)
	if release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), current); errAcquire == nil {
		release()
		t.Fatal("lowering the limit admitted another request before existing leases drained")
	}
}

func TestAuthFileConcurrencyHomeIsManagedAndReadOnly(t *testing.T) {
	h, manager, _ := newConcurrencyManagementFixture(t)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	recorder := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", `{"name":"a.json","concurrency_limit":3}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("Home patch status=%d", recorder.Code)
	}
	recorder = concurrencyManagementRequest(h, http.MethodGet, "/auth-files/concurrency", "")
	for _, fragment := range []string{`"concurrency_managed":true`, `"concurrency_in_flight":null`, `"concurrency_limit":null`} {
		if !strings.Contains(recorder.Body.String(), fragment) {
			t.Fatalf("Home snapshot lacks %s: %s", fragment, recorder.Body.String())
		}
	}
}

func TestAuthFileConcurrencyPatchChecksAuthIndex(t *testing.T) {
	h, manager, record := newConcurrencyManagementFixture(t)
	recorder := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", `{"name":"a.json","auth_index":"wrong","concurrency_limit":3}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("mismatched index status=%d", recorder.Code)
	}
	current, _ := manager.GetByID(record.ID)
	if coreauth.EffectiveConcurrencyLimit(current) != 10 {
		t.Fatal("wrong index modified the credential")
	}
	current.EnsureIndex()
	body := fmt.Sprintf(`{"name":"a.json","auth_index":%q,"concurrency_limit":3}`, current.Index)
	recorder = concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("matched index status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAuthFileConcurrencySnapshotRequiresRuntime(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	recorder := concurrencyManagementRequest(h, http.MethodGet, "/auth-files/concurrency", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing runtime status=%d", recorder.Code)
	}
}

func TestAuthFileConcurrencyConfigPatchPersistsAndReloads(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(path, []byte("# preserve this comment\nport: 8317\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid"}}}
	auths, errSynthesize := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil || len(auths) != 1 {
		t.Fatalf("synthesize: %v, auths=%d", errSynthesize, len(auths))
	}
	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	if _, errRegister := manager.Register(context.Background(), auths[0]); errRegister != nil {
		t.Fatal(errRegister)
	}
	h := NewHandler(cfg, path, manager)
	entry := h.buildAuthFileEntry(auths[0], authFileQuotaContext{})
	if entry == nil || entry["concurrency_config"] != true || entry["runtime_only"] != true {
		t.Fatalf("config credential is missing or not safely marked in the auth list: %v", entry)
	}
	snapshot := concurrencyManagementRequest(h, http.MethodGet, "/auth-files/concurrency", "")
	if !strings.Contains(snapshot.Body.String(), auths[0].ID) {
		t.Fatalf("config credential is missing from occupancy snapshot: %s", snapshot.Body.String())
	}
	var reloaded *config.Config
	h.SetConfigReloadHook(func(_ context.Context, snapshot *config.Config) { reloaded = snapshot })
	for _, raw := range []string{"3", "null"} {
		body := fmt.Sprintf(`{"name":%q,"concurrency_limit":%s}`, auths[0].ID, raw)
		recorder := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("config patch=%d %s", recorder.Code, recorder.Body.String())
		}
		if reloaded == nil {
			t.Fatal("config save did not request runtime reload")
		}
		if raw == "3" && (reloaded.CodexKey[0].ConcurrencyLimit == nil || *reloaded.CodexKey[0].ConcurrencyLimit != 3) {
			t.Fatal("runtime reload has wrong limit")
		}
		if raw == "null" && reloaded.CodexKey[0].ConcurrencyLimit != nil {
			t.Fatal("runtime reload did not restore default")
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil || !strings.Contains(string(data), "# preserve this comment") {
			t.Fatal("config comment was not preserved")
		}
		persisted, errParse := config.ParseConfigBytes(data)
		if errParse != nil {
			t.Fatal(errParse)
		}
		if raw == "3" && (persisted.CodexKey[0].ConcurrencyLimit == nil || *persisted.CodexKey[0].ConcurrencyLimit != 3) {
			t.Fatal("config did not persist the limit")
		}
		if raw == "null" && persisted.CodexKey[0].ConcurrencyLimit != nil {
			t.Fatal("config did not remove the override")
		}
	}
	before := h.cfg
	h.configFilePath = filepath.Join(dir, "missing", "config.yaml")
	recorder := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", fmt.Sprintf(`{"name":%q,"concurrency_limit":4}`, auths[0].ID))
	if recorder.Code != http.StatusInternalServerError || h.cfg != before || h.cfg.CodexKey[0].ConcurrencyLimit != nil {
		t.Fatalf("failed save mutated live config: status=%d", recorder.Code)
	}
}

type concurrencyFailingStore struct {
	coreauth.Store
	fail bool
}

func (s *concurrencyFailingStore) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if s.fail {
		return "", fmt.Errorf("synthetic persistence failure")
	}
	return s.Store.Save(ctx, auth)
}

func TestAuthFileConcurrencySaveFailureDoesNotPublish(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	fileStore := fileauth.NewFileTokenStore()
	fileStore.SetBaseDir(dir)
	store := &concurrencyFailingStore{Store: fileStore}
	manager := coreauth.NewManager(store, nil, nil)
	record, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "fail.json", FileName: "fail.json", Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"path": filepath.Join(dir, "fail.json")},
		Metadata: map[string]any{
			"type": "codex", "access_token": "synthetic-original", "note": "before",
			"headers": map[string]any{"X-Test": "old"}, "priority": float64(2), "weight": 2, "request_retry": float64(1),
		},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	before, errRead := os.ReadFile(filepath.Join(dir, "fail.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager)
	hookCalled := false
	h.postAuthPersistHook = func(context.Context, *coreauth.Auth) error { hookCalled = true; return nil }
	store.fail = true
	body := `{"name":"fail.json","concurrency_limit":3,"note":"after","proxy_url":"direct","headers.X-Test":"new","priority":5,"weight":4,"request_retry":2}`
	recorder := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", body)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("failed save status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	current, _ := manager.GetByID(record.ID)
	if coreauth.EffectiveConcurrencyLimit(current) != 10 || current.Metadata["note"] != "before" || current.ProxyURL != "" || hookCalled {
		t.Fatal("failed save published a partial runtime update or called the success hook")
	}
	if current.Metadata["headers"].(map[string]any)["X-Test"] != "old" || current.Metadata["priority"] != float64(2) {
		t.Fatal("failed patch modified nested metadata before the transaction")
	}
	after, errRead := os.ReadFile(filepath.Join(dir, "fail.json"))
	if errRead != nil || string(before) != string(after) {
		t.Fatal("failed save changed the previous credential file")
	}
	store.fail = false
	recorder = concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", body)
	current, _ = manager.GetByID(record.ID)
	if recorder.Code != http.StatusOK || coreauth.EffectiveConcurrencyLimit(current) != 3 || current.Metadata["note"] != "after" || current.ProxyURL != "direct" || !hookCalled {
		t.Fatalf("retry after failed save did not atomically commit all settings: %d %s", recorder.Code, recorder.Body.String())
	}
	if current.Metadata["headers"].(map[string]any)["X-Test"] != "new" || fmt.Sprint(current.Metadata["priority"]) != "5" || fmt.Sprint(current.Metadata["weight"]) != "4" || fmt.Sprint(current.Metadata["request_retry"]) != "2" {
		t.Fatal("successful mixed edit lost nested fields or numeric control updates")
	}
}

func TestAuthFileConcurrencyPreservesTokensOnlyPresentInNativeStorage(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	storage := &codex.CodexTokenStorage{
		AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", IDToken: "synthetic-id",
		AccountID: "synthetic-account", Email: "synthetic@example.invalid", Type: "codex",
	}
	_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "storage.json", FileName: "storage.json", Provider: "codex", Status: coreauth.StatusActive,
		Storage: storage, Metadata: map[string]any{"type": "codex", "note": "preserve native tokens"},
		Attributes: map[string]string{"path": filepath.Join(dir, "storage.json")},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	var before map[string]any
	beforeData, errBefore := os.ReadFile(filepath.Join(dir, "storage.json"))
	if errBefore != nil {
		t.Fatal(errBefore)
	}
	if errDecode := json.Unmarshal(beforeData, &before); errDecode != nil {
		t.Fatal(errDecode)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager)
	recorder := concurrencyManagementRequest(h, http.MethodPatch, "/auth-files/fields", `{"name":"storage.json","concurrency_limit":3}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("native storage patch=%d %s", recorder.Code, recorder.Body.String())
	}
	data, errRead := os.ReadFile(filepath.Join(dir, "storage.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved map[string]any
	if errDecode := json.Unmarshal(data, &saved); errDecode != nil {
		t.Fatal(errDecode)
	}
	for key, want := range map[string]any{
		"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "id_token": "synthetic-id",
		"account_id": "synthetic-account", "note": "preserve native tokens", "concurrency_limit": float64(3),
	} {
		if saved[key] != want {
			t.Fatalf("stored %s was lost during the concurrency edit", key)
		}
	}
	if _, changed := storage.Metadata["concurrency_limit"]; changed {
		t.Fatal("native storage was modified through a shared pointer")
	}
	for _, field := range []string{"codex_identity_namespace", "codex_identity_version"} {
		if saved[field] != before[field] {
			t.Fatalf("metadata staging changed the credential identity field %s", field)
		}
	}
}
