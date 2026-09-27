package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

type concurrencyListCase struct {
	name      string
	put       func(*Handler, *gin.Context)
	patch     func(*Handler, *gin.Context)
	index     int
	compatKey int
	get       func(*config.Config) *int
}

func concurrencyListCases() []concurrencyListCase {
	return []concurrencyListCase{
		{name: "gemini", put: (*Handler).PutGeminiKeys, patch: (*Handler).PatchGeminiKey, compatKey: -1, get: func(c *config.Config) *int { return c.GeminiKey[0].ConcurrencyLimit }},
		{name: "interactions", put: (*Handler).PutInteractionsKeys, patch: (*Handler).PatchInteractionsKey, compatKey: -1, get: func(c *config.Config) *int { return c.InteractionsKey[0].ConcurrencyLimit }},
		{name: "claude", put: (*Handler).PutClaudeKeys, patch: (*Handler).PatchClaudeKey, compatKey: -1, get: func(c *config.Config) *int { return c.ClaudeKey[0].ConcurrencyLimit }},
		{name: "codex", put: (*Handler).PutCodexKeys, patch: (*Handler).PatchCodexKey, compatKey: -1, get: func(c *config.Config) *int { return c.CodexKey[0].ConcurrencyLimit }},
		{name: "xai", put: (*Handler).PutXAIKeys, patch: (*Handler).PatchXAIKey, compatKey: -1, get: func(c *config.Config) *int { return c.XAIKey[0].ConcurrencyLimit }},
		{name: "meta", put: (*Handler).PutMetaKeys, patch: (*Handler).PatchMetaKey, compatKey: -1, get: func(c *config.Config) *int { return c.MetaKey[0].ConcurrencyLimit }},
		{name: "vertex", put: (*Handler).PutVertexCompatKeys, patch: (*Handler).PatchVertexCompatKey, compatKey: -1, get: func(c *config.Config) *int { return c.VertexCompatAPIKey[0].ConcurrencyLimit }},
		{name: "compat-fallback", put: (*Handler).PutOpenAICompat, patch: (*Handler).PatchOpenAICompat, compatKey: -1, get: func(c *config.Config) *int { return c.OpenAICompatibility[0].ConcurrencyLimit }},
		{name: "compat-key-first", put: (*Handler).PutOpenAICompat, patch: (*Handler).PatchOpenAICompat, index: 1, compatKey: 0, get: func(c *config.Config) *int { return c.OpenAICompatibility[1].APIKeyEntries[0].ConcurrencyLimit }},
		{name: "compat-key-second", put: (*Handler).PutOpenAICompat, patch: (*Handler).PatchOpenAICompat, index: 1, compatKey: 1, get: func(c *config.Config) *int { return c.OpenAICompatibility[1].APIKeyEntries[1].ConcurrencyLimit }},
	}
}

func newConcurrencyListsHandler(t *testing.T) *Handler {
	t.Helper()
	limit := func() *int { n := 7; return &n }
	cfg := &config.Config{
		GeminiKey:          []config.GeminiKey{{APIKey: "gemini-key", BaseURL: "https://gemini.test", ConcurrencyLimit: limit()}},
		InteractionsKey:    []config.GeminiKey{{APIKey: "interactions-key", BaseURL: "https://interactions.test", ConcurrencyLimit: limit()}},
		ClaudeKey:          []config.ClaudeKey{{APIKey: "claude-key", BaseURL: "https://claude.test", ConcurrencyLimit: limit()}},
		CodexKey:           []config.CodexKey{{APIKey: "codex-key", BaseURL: "https://codex.test", ConcurrencyLimit: limit()}},
		XAIKey:             []config.XAIKey{{APIKey: "xai-key", BaseURL: "https://xai.test", ConcurrencyLimit: limit()}},
		MetaKey:            []config.MetaKey{{APIKey: "meta-key", BaseURL: "https://meta.test", ConcurrencyLimit: limit()}},
		VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "vertex-key", BaseURL: "https://vertex.test", ConcurrencyLimit: limit()}},
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "fallback", BaseURL: "https://fallback.test", ConcurrencyLimit: limit()},
			{Name: "compat", BaseURL: "https://compat.test", APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "compat-a", ConcurrencyLimit: limit()},
				{APIKey: "compat-b", ConcurrencyLimit: limit()},
			}},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
	if errSave := config.SaveConfigPreserveComments(h.configFilePath, cfg); errSave != nil {
		t.Fatalf("save initial configuration: %v", errSave)
	}
	return h
}

func (tc concurrencyListCase) patchFields(raw string) map[string]any {
	fields := map[string]any{}
	if raw == "" {
		return fields
	}
	if tc.compatKey < 0 {
		fields["concurrency-limit"] = json.RawMessage(raw)
		return fields
	}
	entries := []map[string]any{
		{"api-key": "compat-a", "concurrency-limit": 7},
		{"api-key": "compat-b", "concurrency-limit": 7},
	}
	entries[tc.compatKey]["concurrency-limit"] = json.RawMessage(raw)
	fields["api-key-entries"] = entries
	return fields
}

func (tc concurrencyListCase) putEntries(raw string) []map[string]any {
	entry := tc.patchFields(raw)
	if tc.name == "compat-fallback" || tc.compatKey >= 0 {
		entries := []map[string]any{
			{"name": "fallback", "base-url": "https://fallback.test", "concurrency-limit": 7},
			{"name": "compat", "base-url": "https://compat.test", "api-key-entries": []map[string]any{
				{"api-key": "compat-a", "concurrency-limit": 7},
				{"api-key": "compat-b", "concurrency-limit": 7},
			}},
		}
		for key, value := range entry {
			entries[tc.index][key] = value
		}
		return entries
	}
	entry["api-key"] = tc.name + "-key"
	entry["base-url"] = "https://" + tc.name + ".test"
	return []map[string]any{entry}
}

func invokeConcurrencyListRequest(t *testing.T, h *Handler, method string, call func(*Handler, *gin.Context), body any) *httptest.ResponseRecorder {
	t.Helper()
	data, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/v0/management/keys", bytes.NewReader(data))
	ctx.Request.Header.Set("Content-Type", "application/json")
	call(h, ctx)
	return rec
}

func assertConcurrencyListsLimit(t *testing.T, tc concurrencyListCase, h *Handler, raw string) {
	t.Helper()
	var want *int
	if raw != "null" {
		n := 7
		if raw != "" {
			if errDecode := json.Unmarshal([]byte(raw), &n); errDecode != nil {
				t.Fatal(errDecode)
			}
		}
		want = &n
	}
	data, errRead := os.ReadFile(h.configFilePath)
	if errRead != nil {
		t.Fatalf("read persisted configuration: %v", errRead)
	}
	var persisted config.Config
	if errDecode := yaml.Unmarshal(data, &persisted); errDecode != nil {
		t.Fatalf("decode persisted configuration: %v", errDecode)
	}
	for name, cfg := range map[string]*config.Config{"memory": h.cfg, "disk": &persisted} {
		if got := tc.get(cfg); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s concurrency-limit = %v, want %v", name, got, want)
		}
		for _, other := range concurrencyListCases() {
			if other.name == tc.name {
				continue
			}
			if got := other.get(cfg); got == nil || *got != 7 {
				t.Fatalf("%s changed unrelated %s concurrency-limit = %v", name, other.name, got)
			}
		}
	}
}

func TestConfigConcurrencyListsPatchLimits(t *testing.T) {
	for _, tc := range concurrencyListCases() {
		for _, raw := range []string{"1", "3", "1000000", "null", ""} {
			name := raw
			if name == "" {
				name = "omitted"
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				h := newConcurrencyListsHandler(t)
				fields := tc.patchFields(raw)
				fields["prefix"] = "updated"
				body := map[string]any{"index": tc.index, "value": fields}
				rec := invokeConcurrencyListRequest(t, h, http.MethodPatch, tc.patch, body)
				if rec.Code != http.StatusOK {
					t.Fatalf("PATCH status = %d, want 200; body=%s", rec.Code, rec.Body.String())
				}
				assertConcurrencyListsLimit(t, tc, h, raw)
			})
		}
	}
}

func TestConfigConcurrencyListsPutLimits(t *testing.T) {
	for _, tc := range concurrencyListCases() {
		for _, raw := range []string{"1", "3", "1000000", "null"} {
			for _, wrapped := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/wrapped=%t", tc.name, raw, wrapped), func(t *testing.T) {
					h := newConcurrencyListsHandler(t)
					var body any = tc.putEntries(raw)
					if wrapped {
						body = map[string]any{"items": body}
					}
					rec := invokeConcurrencyListRequest(t, h, http.MethodPut, tc.put, body)
					if rec.Code != http.StatusOK {
						t.Fatalf("PUT status = %d, want 200; body=%s", rec.Code, rec.Body.String())
					}
					assertConcurrencyListsLimit(t, tc, h, raw)
				})
			}
		}
	}
}

func assertConcurrencyListsRejectedAtomically(t *testing.T, h *Handler, method string, call func(*Handler, *gin.Context), body any) {
	t.Helper()
	beforeConfig := h.cfg.CloneForRuntime()
	beforeDisk, errRead := os.ReadFile(h.configFilePath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	rec := invokeConcurrencyListRequest(t, h, method, call, body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("%s status = %d, want 400; body=%s", method, rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(h.cfg, beforeConfig) {
		t.Error("rejected concurrency-limit changed the in-memory configuration")
	}
	afterDisk, errRead := os.ReadFile(h.configFilePath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !bytes.Equal(beforeDisk, afterDisk) {
		t.Error("rejected concurrency-limit changed the configuration file")
	}
	if h.reloadGeneration != 0 {
		t.Errorf("rejected concurrency-limit scheduled reload generation %d", h.reloadGeneration)
	}
}

func TestConfigConcurrencyListsRejectInvalidAtomically(t *testing.T) {
	for _, tc := range concurrencyListCases() {
		for _, raw := range []string{"0", "-1", "1000001", "3.5", `"3"`, "true"} {
			for _, mutation := range []string{"fields", "empty-base-url", "empty-api-key"} {
				t.Run(tc.name+"/"+raw+"/PATCH/"+mutation, func(t *testing.T) {
					h := newConcurrencyListsHandler(t)
					fields := tc.patchFields(raw)
					fields["prefix"] = "must-not-apply"
					fields["headers"] = map[string]string{"X-Test": "must-not-apply"}
					fields["disable-cooling"] = true
					if mutation == "empty-base-url" {
						fields["base-url"] = ""
					}
					if mutation == "empty-api-key" {
						fields["api-key"] = ""
						if tc.compatKey >= 0 {
							entries := fields["api-key-entries"].([]map[string]any)
							entries[tc.compatKey]["api-key"] = ""
						}
					}
					assertConcurrencyListsRejectedAtomically(t, h, http.MethodPatch, tc.patch, map[string]any{"index": tc.index, "value": fields})
				})
				for _, wrapped := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/PUT/%s/wrapped=%t", tc.name, raw, mutation, wrapped), func(t *testing.T) {
						h := newConcurrencyListsHandler(t)
						entries := tc.putEntries(raw)
						entry := entries[tc.index]
						entry["prefix"] = "must-not-apply"
						entry["headers"] = map[string]string{"X-Test": "must-not-apply"}
						if mutation == "empty-base-url" {
							entry["base-url"] = ""
						}
						if mutation == "empty-api-key" {
							entry["api-key"] = ""
							if tc.compatKey >= 0 {
								keys := entry["api-key-entries"].([]map[string]any)
								keys[tc.compatKey]["api-key"] = ""
							}
						}
						// A valid earlier item must not be applied before a later invalid item is rejected.
						valid := map[string]any{"api-key": "new-key", "name": "new-provider", "base-url": "https://new.test", "concurrency-limit": 3}
						entries = append([]map[string]any{valid}, entries...)
						var body any = entries
						if wrapped {
							body = map[string]any{"items": entries}
						}
						assertConcurrencyListsRejectedAtomically(t, h, http.MethodPut, tc.put, body)
					})
				}
			}
		}
	}
}
