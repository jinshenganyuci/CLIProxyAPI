package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestGetOpenAICompatPreservesConcurrencyLimits(t *testing.T) {
	fallback, first, second := 3, 4, 5
	h := &Handler{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{Name: "fallback", BaseURL: "http://fallback.invalid", ConcurrencyLimit: &fallback},
		{Name: "keys", BaseURL: "http://keys.invalid", APIKeyEntries: []config.OpenAICompatibilityAPIKey{
			{APIKey: "first", ConcurrencyLimit: &first}, {APIKey: "second", ConcurrencyLimit: &second}, {APIKey: "default"},
		}},
	}}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)
	h.GetOpenAICompat(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d", recorder.Code)
	}
	var payload struct {
		Providers []config.OpenAICompatibility `json:"openai-compatibility"`
	}
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(payload.Providers) != 2 || payload.Providers[0].ConcurrencyLimit == nil || *payload.Providers[0].ConcurrencyLimit != fallback {
		t.Fatalf("fallback concurrency limit missing from GET: %s", recorder.Body.String())
	}
	keys := payload.Providers[1].APIKeyEntries
	if len(keys) != 3 || keys[0].ConcurrencyLimit == nil || *keys[0].ConcurrencyLimit != first || keys[1].ConcurrencyLimit == nil || *keys[1].ConcurrencyLimit != second || keys[2].ConcurrencyLimit != nil {
		t.Fatalf("key concurrency limits missing from GET: %s", recorder.Body.String())
	}
}
