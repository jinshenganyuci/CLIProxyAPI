package management

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8APIKeyNotesMutationPreservesKeysAndETag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	hash := config.APIKeyNoteID("synthetic-client-a")
	raw := fmt.Sprintf("config-version: 8\nserver: {port: 8317}\naccess:\n  api-keys: [synthetic-client-a, synthetic-client-b]\n  api-key-notes: {'%s': Original}\nrouting: {strategy: round-robin}\n", hash)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch} {
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}
	do := func(method, suffix, body, etag string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/v8/management/config/"+suffix, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	get := do(http.MethodGet, "access", "", "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") == "" {
		t.Fatal("access read did not supply its configuration revision")
	}
	initialETag := get.Header().Get("ETag")
	update := fmt.Sprintf(`{"api-key-notes":{"%s":"手机 Phone"}}`, hash)
	response := do(http.MethodPatch, "access", update, initialETag)
	if response.Code != http.StatusOK {
		t.Fatalf("note mutation status=%d body=%s", response.Code, response.Body.String())
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKeyNote("synthetic-client-a") != "手机 Phone" || !reflect.DeepEqual(loaded.APIKeys, cfg.APIKeys) || loaded.Port != cfg.Port || loaded.Routing.Strategy != cfg.Routing.Strategy {
		t.Fatal("note-only mutation changed key authorization or unrelated configuration")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stale := do(http.MethodPatch, "access", update, initialETag)
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatal("stale note edit was not rejected")
	}
	currentETag := response.Header().Get("ETag")
	for _, value := range []string{`true`, `"` + strings.Repeat("长", 121) + `"`} {
		invalid := do(http.MethodPatch, "access", fmt.Sprintf(`{"api-key-notes":{"%s":%s}}`, hash, value), currentETag)
		if invalid.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid note status=%d", invalid.Code)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected note edits modified persisted configuration")
	}
	clear := do(http.MethodPut, "access/api-key-notes", `{}`, currentETag)
	if clear.Code != http.StatusOK {
		t.Fatalf("clear notes status=%d body=%s", clear.Code, clear.Body.String())
	}
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeyNotes) != 0 || !reflect.DeepEqual(loaded.APIKeys, cfg.APIKeys) {
		t.Fatal("clearing notes failed or changed key authorization")
	}
}
