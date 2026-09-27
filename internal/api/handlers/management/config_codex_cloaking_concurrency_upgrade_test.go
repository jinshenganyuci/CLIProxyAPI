package management

import (
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
	configdiff "github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func patchCodexUpgrade(h *Handler, value string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/codex-api-key", strings.NewReader(`{"index":0,"value":`+value+`}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchCodexKey(ctx)
	return rec
}

func synthesizeCodexUpgrade(t *testing.T, cfg *config.Config) *coreauth.Auth {
	t.Helper()
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Unix(1800000000, 0), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil || len(auths) != 1 {
		t.Fatalf("synthesis=%d/%v", len(auths), err)
	}
	return auths[0]
}

func TestCodexCloakingPriorityConcurrencyRoundTripUpgrade(t *testing.T) {
	for _, raw := range []string{"true", "false", "null"} {
		t.Run(raw, func(t *testing.T) {
			limit, disableCooling := 3, true
			cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "synthetic-key", BaseURL: "https://codex.invalid/v1", ProxyURL: "socks5://proxy.invalid:1080", Prefix: "fork", Websockets: true, AlphaSearch: true, Priority: 9, ConcurrencyLimit: &limit, DisableCooling: &disableCooling, Headers: map[string]string{"X-Synthetic": "identity"}}}}
			cfg.Codex.DisableCodexCloaking = true
			oldCfg := cfg.CloneForRuntime()
			beforeAuth := synthesizeCodexUpgrade(t, cfg)
			h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			response := patchCodexUpgrade(h, fmt.Sprintf(`{"priority":-7,"concurrency-limit":5,"disable-codex-cloaking":%s}`, raw))
			if response.Code != http.StatusOK {
				t.Fatalf("patch=%d/%s", response.Code, response.Body.String())
			}
			loaded, err := config.LoadConfigOptional(h.configFilePath, false)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.CodexKey[0], loaded.CodexKey[0]) || !loaded.Codex.DisableCodexCloaking {
				t.Fatal("runtime and persisted credential controls differ")
			}
			afterAuth := synthesizeCodexUpgrade(t, loaded)
			if afterAuth.ID != beforeAuth.ID || afterAuth.ProxyURL != beforeAuth.ProxyURL || afterAuth.AuthKind() != coreauth.AuthKindAPIKey || afterAuth.Attributes["header:X-Synthetic"] != beforeAuth.Attributes["header:X-Synthetic"] {
				t.Fatal("control patch changed proxy or credential identity")
			}
			if coreauth.EffectiveConcurrencyLimit(afterAuth) != 5 || afterAuth.Attributes["priority"] != "-7" || afterAuth.Attributes["websockets"] != "true" || afterAuth.Attributes[coreauth.AttributeCodexAlphaSearch] != "true" || afterAuth.Metadata["disable_cooling"] != true {
				t.Fatalf("synthesis lost controls: %+v", afterAuth)
			}
			wantCloaking := raw
			if raw == "null" {
				wantCloaking = ""
			}
			if afterAuth.Attributes[coreauth.AttributeCodexDisableCloaking] != wantCloaking {
				t.Fatalf("cloaking attribute=%q, want%q", afterAuth.Attributes[coreauth.AttributeCodexDisableCloaking], wantCloaking)
			}
			changes := strings.Join(configdiff.BuildConfigChangeDetails(oldCfg, loaded), "\n")
			if !strings.Contains(changes, "concurrency-limit") || (raw != "null" && !strings.Contains(changes, "disable-codex-cloaking")) {
				t.Fatalf("watcher omitted combined control changes: %s", changes)
			}
			clone := loaded.CloneForRuntime()
			if clone.CodexKey[0].DisableCodexCloaking != nil {
				*clone.CodexKey[0].DisableCodexCloaking = !*clone.CodexKey[0].DisableCodexCloaking
				if *loaded.CodexKey[0].DisableCodexCloaking != (raw == "true") {
					t.Fatal("runtime clone shared cloaking pointer")
				}
			}
			expected := cfg.CloneForRuntime()
			diskBefore, _ := os.ReadFile(h.configFilePath)
			for _, invalid := range []string{`{"priority":1.5,"concurrency-limit":2,"disable-codex-cloaking":false}`, `{"priority":4,"concurrency-limit":0,"disable-codex-cloaking":false}`, `{"priority":4,"concurrency-limit":2,"disable-codex-cloaking":"invalid"}`} {
				response := patchCodexUpgrade(h, invalid)
				if response.Code != http.StatusBadRequest {
					t.Fatalf("invalid combined patch accepted: %s status%d", invalid, response.Code)
				}
				diskAfter, _ := os.ReadFile(h.configFilePath)
				if !reflect.DeepEqual(cfg.CodexKey, expected.CodexKey) || string(diskAfter) != string(diskBefore) {
					t.Fatal("invalid patch partially changed controls or disk")
				}
			}
			response = patchCodexUpgrade(h, `{"disable-codex-cloaking":null}`)
			if response.Code != http.StatusOK {
				t.Fatalf("reset=%d/%s", response.Code, response.Body.String())
			}
			loaded, err = config.LoadConfigOptional(h.configFilePath, false)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.CodexKey[0].DisableCodexCloaking != nil || !loaded.Codex.DisableCodexCloaking {
				t.Fatal("reset did not restore inheritance from the global setting")
			}
		})
	}
}

func TestCodexInvalidCloakingDoesNotDeleteCredentialUpgrade(t *testing.T) {
	limit := 3
	cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "synthetic-key", BaseURL: "https://codex.invalid", ConcurrencyLimit: &limit}}}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
	before := cfg.CloneForRuntime()
	beforeDisk, _ := os.ReadFile(h.configFilePath)
	response := patchCodexUpgrade(h, `{"base-url":"","disable-codex-cloaking":"invalid"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid cloaking accepted during deletion: status=%d", response.Code)
	}
	afterDisk, _ := os.ReadFile(h.configFilePath)
	if !reflect.DeepEqual(cfg.CodexKey, before.CodexKey) || string(afterDisk) != string(beforeDisk) {
		t.Fatal("invalid cloaking deleted a credential")
	}
}
