package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAuthCooldownProjectionPreservesConcurrencyAndSource(t *testing.T) {
	for _, mode := range []string{"expired-cooldown", "unauthorized", "transient-unauthorized", "token-expired", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			h, manager, record := newConcurrencyManagementFixture(t)
			release, errAcquire := manager.AcquireCredentialConcurrency(context.Background(), record)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer release()
			current, _ := manager.GetByID(record.ID)
			current.Status = coreauth.StatusError
			current.Unavailable = true
			current.StatusMessage = "expired cooldown"
			current.NextRetryAfter = time.Now().Add(-time.Hour)
			switch mode {
			case "unauthorized":
				current.LastError = &coreauth.Error{HTTPStatus: http.StatusUnauthorized, Message: "synthetic unauthorized"}
				current.NextRetryAfter = time.Time{}
			case "transient-unauthorized":
				current.LastError = &coreauth.Error{HTTPStatus: http.StatusUnauthorized, Message: "synthetic retryable unauthorized"}
			case "token-expired":
				current.StatusMessage = "token expired"
				current.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
			case "disabled":
				current.Disabled = true
			}
			if _, errUpdate := manager.Update(context.Background(), current); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			before, _ := manager.GetByID(record.ID)
			onDisk, errRead := os.ReadFile(record.Attributes["path"])
			if errRead != nil {
				t.Fatal(errRead)
			}
			entry := h.buildAuthFileEntry(before.Clone())
			if entry["concurrency_in_flight"] != 1 || entry["concurrency_limit"] != 10 {
				t.Fatalf("status projection changed capacity: %#v", entry)
			}
			wantStatus := coreauth.StatusError
			wantUnavailable := true
			if mode == "expired-cooldown" || mode == "transient-unauthorized" {
				wantStatus, wantUnavailable = coreauth.StatusActive, false
			} else if mode == "disabled" {
				wantStatus = coreauth.StatusDisabled
				wantUnavailable = before.Unavailable
			}
			if entry["status"] != wantStatus || entry["unavailable"] != wantUnavailable {
				t.Fatalf("projected status = %v/%v, want %v/%v", entry["status"], entry["unavailable"], wantStatus, wantUnavailable)
			}
			after, _ := manager.GetByID(record.ID)
			afterDisk, errRead := os.ReadFile(record.Attributes["path"])
			if errRead != nil || string(afterDisk) != string(onDisk) || !reflect.DeepEqual(before, after) {
				t.Fatal("read-only cooldown projection mutated auth or persisted credentials")
			}
		})
	}
}

func TestClaudePriorityPatchPreservesForkControlsAtomically(t *testing.T) {
	for _, priority := range []int{0, -7} {
		t.Run(fmt.Sprint(priority), func(t *testing.T) {
			limit, cache := 3, true
			cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "synthetic-key", Priority: 8,
				ConcurrencyLimit: &limit, FingerprintProfile: "claude-code-cli", ProxyURL: "socks5://proxy.invalid:1080",
				Cloak: &config.CloakConfig{Mode: "always", StrictMode: true, CacheUserID: &cache, SensitiveWords: []string{"synthetic-word"}},
			}}}
			h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			patch := func(value string) *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key", strings.NewReader(`{"index":0,"value":`+value+`}`))
				ctx.Request.Header.Set("Content-Type", "application/json")
				h.PatchClaudeKey(ctx)
				return recorder
			}
			want := cfg.ClaudeKey[0]
			want.Priority = priority
			if response := patch(fmt.Sprintf(`{"priority":%d}`, priority)); response.Code != http.StatusOK {
				t.Fatalf("priority patch = %d: %s", response.Code, response.Body.String())
			}
			if !reflect.DeepEqual(cfg.ClaudeKey[0], want) {
				t.Fatalf("priority changed credential controls: %+v", cfg.ClaudeKey[0])
			}
			loaded, errLoad := config.LoadConfigOptional(h.configFilePath, false)
			if errLoad != nil || !reflect.DeepEqual(loaded.ClaudeKey[0], want) {
				t.Fatalf("persisted priority/control mismatch: %v", errLoad)
			}
			beforeDisk, _ := os.ReadFile(h.configFilePath)
			for _, invalid := range []string{
				`{"priority":1.5,"concurrency-limit":2}`,
				`{"priority":9,"concurrency-limit":0,"cloak":{"mode":"never"}}`,
				`{"priority":9,"concurrency-limit":2,"cloak":{"strict-mode":"invalid"}}`,
			} {
				if response := patch(invalid); response.Code != http.StatusBadRequest {
					t.Fatalf("invalid patch accepted: %s: %d", invalid, response.Code)
				}
				afterDisk, _ := os.ReadFile(h.configFilePath)
				if !reflect.DeepEqual(cfg.ClaudeKey[0], want) || string(beforeDisk) != string(afterDisk) {
					t.Fatalf("rejected patch partially updated priority/cloak/concurrency: %s", invalid)
				}
			}
		})
	}
}
