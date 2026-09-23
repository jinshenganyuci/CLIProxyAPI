package redisqueue_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestUpgrade7315UsageCapturesTrustedIPWithoutChangingPeerOrBilling(t *testing.T) {
	for _, tt := range []struct {
		name, wantIP string
		trusted      []string
	}{
		{name: "default ignores forwarded address", wantIP: "192.0.2.10"},
		{name: "trusted proxy resolves client", wantIP: "203.0.113.5", trusted: []string{"192.0.2.0/24"}},
		{name: "untrusted proxy ignores forwarded address", wantIP: "192.0.2.10", trusted: []string{"198.51.100.0/24"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wasEnabled, wasUsageEnabled := redisqueue.Enabled(), redisqueue.UsageStatisticsEnabled()
			redisqueue.SetEnabled(true)
			redisqueue.SetUsageStatisticsEnabled(true)
			defer func() { redisqueue.SetEnabled(wasEnabled); redisqueue.SetUsageStatisticsEnabled(wasUsageEnabled) }()
			events, unsubscribe := redisqueue.SubscribeUsage()
			defer unsubscribe()
			<-events // Consume the subscription capability notification.
			gin.SetMode(gin.TestMode)
			c, engine := gin.CreateTestContext(httptest.NewRecorder())
			if err := engine.SetTrustedProxies(tt.trusted); err != nil {
				t.Fatal(err)
			}
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.RemoteAddr = "192.0.2.10:1234"
			c.Request.Header.Set("X-Forwarded-For", "203.0.113.5")
			c.Request.Header.Set("Session-Id", "73150000-0000-4000-8000-000000000001")
			h := &handlers.BaseAPIHandler{Cfg: &config.SDKConfig{}}
			ctx, cancel := h.GetContextWithCancel(nil, c, context.Background())
			defer cancel()

			// Asynchronous consumers must use metadata captured before Gin request recycling.
			c.Request = httptest.NewRequest(http.MethodGet, "/recycled", nil)
			c.Request.RemoteAddr = "127.0.0.1:1234"
			coreusage.PublishRecord(ctx, coreusage.Record{
				Provider: "codex", Model: "synthetic-model", APIKey: "synthetic-key",
				Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
			})
			var raw []byte
			select {
			case raw = <-events:
			case <-time.After(5 * time.Second):
				t.Fatal("usage event not delivered")
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{
				"client_ip": "192.0.2.10", "resolved_client_ip": tt.wantIP,
				"x_forwarded_for": "203.0.113.5", "session_id": "73150000-0000-4000-8000-000000000001",
			} {
				if payload[key] != want {
					t.Fatalf("%s = %v, want %s", key, payload[key], want)
				}
			}
			tokens, ok := payload["tokens"].(map[string]any)
			if !ok || tokens["total_tokens"] != float64(30) {
				t.Fatalf("unexpected billing: %v", payload["tokens"])
			}
			if _, ok := payload["user_api_key"]; ok {
				t.Fatal("legacy absent field unexpectedly present")
			}
		})
	}
}
