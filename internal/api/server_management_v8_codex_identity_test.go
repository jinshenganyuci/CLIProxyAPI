package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

const v8IdentityRouteKey = "synthetic-v8-identity-route-key"

func newV8IdentityRouteServer(t *testing.T, mode string) (*Server, string, map[string]map[string]any, *coreauth.Manager) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("port: 8317\nremote-management: {secret-key: "+v8IdentityRouteKey+"}\ncodex: {identity-confuse: false}\n"), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	cfg.AuthDir = t.TempDir()
	cfg.Home.Enabled = mode == "home"
	manager := coreauth.NewManager(nil, nil, nil)
	original := map[string]map[string]any{}
	for index, name := range []string{"codex-route-a.json", "codex-route-b.json"} {
		proxy := "direct"
		if index == 1 {
			proxy = "socks5h://127.0.0.1:1080"
		}
		metadata := map[string]any{
			"type": "codex", "email": name + "@example.invalid", "access_token": "synthetic-access-" + name,
			"refresh_token": "synthetic-refresh-" + name, "proxy_url": proxy, "retained_field": "unchanged",
		}
		if index == 0 {
			metadata["concurrency_limit"] = float64(3)
		}
		raw, errMarshal := json.Marshal(metadata)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		authPath := filepath.Join(cfg.AuthDir, name)
		if errWrite := os.WriteFile(authPath, raw, 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
		original[name] = metadata
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID: name, FileName: name, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: proxy,
			Metadata:   metadata,
			Attributes: map[string]string{coreauth.AttributePath: authPath, coreauth.AttributeSource: authPath, coreauth.AttributeSourceBackend: coreauth.AuthSourceFile},
		}); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	h := management.NewHandler(cfg, path, manager)
	h.SetLocalPassword(v8IdentityRouteKey)
	server := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	server.managementRoutesEnabled.Store(mode != "disabled")
	server.registerManagementRoutes()
	return server, path, original, manager
}

func callV8IdentityRoute(t *testing.T, server *Server, method, path, body, key string, want int) map[string]any {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response := httptest.NewRecorder()
	server.engine.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, want, response.Body.String())
	}
	if response.Code == http.StatusNotFound {
		return nil
	}
	var value map[string]any
	if errDecode := json.Unmarshal(response.Body.Bytes(), &value); errDecode != nil {
		t.Fatal(errDecode)
	}
	return value
}

func readV8IdentityRouteFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	return raw
}

func TestManagementV8CodexIdentityRoutesRequireManagementAccess(t *testing.T) {
	for _, mode := range []string{"missing-key", "wrong-key", "disabled", "home"} {
		t.Run(mode, func(t *testing.T) {
			server, path, originals, _ := newV8IdentityRouteServer(t, mode)
			before := map[string][]byte{path: readV8IdentityRouteFile(t, path)}
			for name := range originals {
				authPath := filepath.Join(server.cfg.AuthDir, name)
				before[authPath] = readV8IdentityRouteFile(t, authPath)
			}
			key, want := v8IdentityRouteKey, http.StatusNotFound
			if mode == "missing-key" {
				key, want = "", http.StatusUnauthorized
			} else if mode == "wrong-key" {
				key, want = "synthetic-wrong-key", http.StatusUnauthorized
			}
			for _, route := range []struct{ method, suffix, body string }{
				{http.MethodGet, "", ""},
				{http.MethodPut, "", `{"enabled":false}`},
				{http.MethodPost, "/initialize", `{}`},
				{http.MethodPost, "/rotate", `{"name":"codex-route-a.json","confirm":"ROTATE"}`},
			} {
				callV8IdentityRoute(t, server, route.method, "/v8/management/credentials/codex-identity"+route.suffix, route.body, key, want)
			}
			for file, raw := range before {
				if !bytes.Equal(raw, readV8IdentityRouteFile(t, file)) {
					t.Fatalf("denied route modified %s", filepath.Base(file))
				}
			}
		})
	}
}

func TestManagementV8CodexIdentityRoutesPersistAndMatchV0(t *testing.T) {
	server, path, originals, manager := newV8IdentityRouteServer(t, "authorized")
	const v8Path = "/v8/management/credentials/codex-identity"
	request := func(method, endpoint, body string, want int) map[string]any {
		return callV8IdentityRoute(t, server, method, endpoint, body, v8IdentityRouteKey, want)
	}
	legacyConfig := readV8IdentityRouteFile(t, path)
	initial := request(http.MethodGet, v8Path, "", http.StatusOK)
	if initial["missing"] != float64(2) || initial["all_ready"] != false || initial["enabled"] != false {
		t.Fatalf("unexpected initial status: %v", initial)
	}
	request(http.MethodPut, v8Path, `{"enabled":true}`, http.StatusConflict)
	if !bytes.Equal(legacyConfig, readV8IdentityRouteFile(t, path)) {
		t.Fatal("read or rejected enable migrated legacy configuration")
	}
	initialized := request(http.MethodPost, v8Path+"/initialize", `{}`, http.StatusOK)
	if initialized["initialized"] != float64(2) {
		t.Fatalf("initialized = %v, want 2", initialized["initialized"])
	}
	if !bytes.Equal(legacyConfig, readV8IdentityRouteFile(t, path)) {
		t.Fatal("credential initialization unexpectedly rewrote configuration")
	}
	readMetadata := func(name string) map[string]any {
		t.Helper()
		var metadata map[string]any
		if errDecode := json.Unmarshal(readV8IdentityRouteFile(t, filepath.Join(server.cfg.AuthDir, name)), &metadata); errDecode != nil {
			t.Fatal(errDecode)
		}
		return metadata
	}
	namespaces := map[string]string{}
	initializedFiles := map[string][]byte{}
	for name := range originals {
		namespace, version, errParse := codex.ParseCredentialIdentity(readMetadata(name))
		if errParse != nil || version != codex.CredentialIdentityCurrentVersion {
			t.Fatalf("invalid initialized identity: version=%d error=%v", version, errParse)
		}
		namespaces[name] = namespace.String()
		initializedFiles[name] = readV8IdentityRouteFile(t, filepath.Join(server.cfg.AuthDir, name))
	}
	if namespaces["codex-route-a.json"] == namespaces["codex-route-b.json"] {
		t.Fatal("two credentials received the same namespace")
	}
	idempotent := request(http.MethodPost, v8Path+"/initialize", `{}`, http.StatusOK)
	if idempotent["initialized"] != float64(0) {
		t.Fatal("second initialization was not idempotent")
	}
	request(http.MethodPut, v8Path, `{"enabled":true,"synthesize_missing_installation_id":false,"credential_proxy_policy":"require"}`, http.StatusOK)
	var persisted map[string]any
	if errDecode := yaml.Unmarshal(readV8IdentityRouteFile(t, path), &persisted); errDecode != nil {
		t.Fatal(errDecode)
	}
	if _, legacy := persisted["codex"]; legacy {
		t.Fatal("successful v8 update retained legacy codex configuration")
	}
	oauth, okOAuth := persisted["oauth"].(map[string]any)
	providers, okProviders := oauth["providers"].(map[string]any)
	codexConfig, okCodex := providers["codex"].(map[string]any)
	identity, okIdentity := codexConfig["credential-identity"].(map[string]any)
	if !okOAuth || !okProviders || !okCodex || !okIdentity || identity["enabled"] != true || codexConfig["credential-proxy-policy"] != "require" {
		t.Fatalf("v8 credential identity settings were not persisted: %v", persisted)
	}
	loaded, errLoad := config.LoadConfig(path)
	if errLoad != nil || !loaded.Codex.CredentialIdentity.Enabled || loaded.Codex.CredentialProxyPolicy != "require" {
		t.Fatalf("reloaded identity configuration lost settings: %v", errLoad)
	}
	request(http.MethodPost, v8Path+"/rotate", `{"name":"codex-route-a.json","confirm":"no"}`, http.StatusBadRequest)
	for name, raw := range initializedFiles {
		if !bytes.Equal(raw, readV8IdentityRouteFile(t, filepath.Join(server.cfg.AuthDir, name))) {
			t.Fatalf("idempotent initialize, enable, or rejected rotation modified %s", name)
		}
	}
	rotated := request(http.MethodPost, v8Path+"/rotate", `{"name":"codex-route-a.json","confirm":"ROTATE"}`, http.StatusOK)
	if rotated["sessions_reset"] != true {
		t.Fatal("rotation did not report session invalidation")
	}
	if readMetadata("codex-route-a.json")[codex.CredentialIdentityNamespaceMetadataKey] == namespaces["codex-route-a.json"] {
		t.Fatal("target namespace did not rotate")
	}
	if !bytes.Equal(initializedFiles["codex-route-b.json"], readV8IdentityRouteFile(t, filepath.Join(server.cfg.AuthDir, "codex-route-b.json"))) {
		t.Fatal("rotation rewrote the other credential")
	}
	for name, original := range originals {
		metadata := readMetadata(name)
		stored, found := manager.GetByID(name)
		if !found || stored.ProxyURL != original["proxy_url"] || !reflect.DeepEqual(stored.Metadata, metadata) {
			t.Fatalf("runtime credential differs from persisted metadata for %s", name)
		}
		delete(metadata, codex.CredentialIdentityNamespaceMetadataKey)
		delete(metadata, codex.CredentialIdentityVersionMetadataKey)
		if !reflect.DeepEqual(metadata, original) {
			t.Fatalf("identity operation modified unrelated credential fields for %s", name)
		}
	}
	v8Status := request(http.MethodGet, v8Path, "", http.StatusOK)
	v0Status := request(http.MethodGet, "/v0/management/codex-credential-identity", "", http.StatusOK)
	if !reflect.DeepEqual(v8Status, v0Status) || v8Status["enabled"] != true || v8Status["ready"] != float64(2) || v8Status["all_ready"] != true {
		t.Fatalf("v0/v8 identity status mismatch: v8=%v v0=%v", v8Status, v0Status)
	}
	statusRaw, errMarshal := json.Marshal(v8Status)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for name, original := range originals {
		for _, private := range []string{original["access_token"].(string), original["refresh_token"].(string), readMetadata(name)[codex.CredentialIdentityNamespaceMetadataKey].(string)} {
			if bytes.Contains(statusRaw, []byte(private)) {
				t.Fatal("identity status exposed a token or namespace")
			}
		}
	}
	counters := request(http.MethodGet, "/v8/management/credentials/concurrency", "", http.StatusOK)
	entries, okEntries := counters["entries"].([]any)
	if !okEntries || len(entries) != 2 {
		t.Fatalf("missing concurrency counters: %v", counters)
	}
	for _, item := range entries {
		entry := item.(map[string]any)
		want := float64(10)
		if entry["id"] == "codex-route-a.json" {
			want = 3
		}
		if entry["concurrency_limit"] != want || entry["concurrency_in_flight"] != float64(0) {
			t.Fatalf("identity operation changed concurrency: %v", entry)
		}
	}
}
