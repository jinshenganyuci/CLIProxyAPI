package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPersistRejectsInvalidCredentialConcurrencyLimit(t *testing.T) {
	path := writeTestConfigFile(t)
	before, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	invalid := 0
	h := &Handler{cfg: &config.Config{CodexKey: []config.CodexKey{{APIKey: "test", ConcurrencyLimit: &invalid}}}, configFilePath: path}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config", nil)
	h.mu.Lock()
	persisted := h.persistLocked(ctx)
	h.mu.Unlock()
	if persisted || recorder.Code != http.StatusBadRequest {
		t.Fatalf("persist returned status %d, want 400", recorder.Code)
	}
	after, errRead := os.ReadFile(path)
	if errRead != nil || string(after) != string(before) {
		t.Fatalf("invalid concurrency config was persisted: %v", errRead)
	}
}
