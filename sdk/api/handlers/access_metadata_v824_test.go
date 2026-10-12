package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestV824ExecutionContextPreservesAuthenticatedUsageIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		native         bool
		parentOverride bool
	}{
		{name: "native", provider: "config-inline", native: true},
		{name: "plugin", provider: "access-guard", native: false},
		{name: "legacy-gin", provider: "config-inline", native: true},
		{name: "anonymous", native: false},
		{name: "nil-gin", native: false},
		{name: "explicit-parent-false", provider: "config-inline", native: true, parentOverride: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			incoming := usage.WithAPIKey(t.Context(), "synthetic-authenticated")
			incoming = usage.WithAccessProvider(incoming, tc.provider)
			incoming = usage.WithIsNativeKey(incoming, tc.native)
			if tc.name == "legacy-gin" {
				incoming = t.Context()
			}
			ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(incoming)
			ginCtx.Set("userApiKey", "synthetic-authenticated")
			ginCtx.Set("accessProvider", tc.provider)
			parent := context.Background()
			wantKey, wantProvider, wantNative := "synthetic-authenticated", tc.provider, tc.native
			if tc.name == "anonymous" {
				ginCtx.Request = ginCtx.Request.WithContext(t.Context())
				delete(ginCtx.Keys, "userApiKey")
				delete(ginCtx.Keys, "accessProvider")
				wantKey = ""
			}
			if tc.parentOverride {
				parent = usage.WithAPIKey(parent, "synthetic-parent")
				parent = usage.WithAccessProvider(parent, "internal-plugin")
				parent = usage.WithIsNativeKey(parent, false)
				wantKey, wantProvider, wantNative = "synthetic-parent", "internal-plugin", false
			}
			if tc.name == "nil-gin" {
				ginCtx = nil
				wantKey = ""
			}
			handler := NewBaseAPIHandlers(&config.SDKConfig{}, nil)
			ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, parent)
			defer cancel()
			// Deferred usage must not pick up a later request's pooled Gin context.
			if ginCtx != nil {
				ginCtx.Set("userApiKey", "synthetic-later-request")
				ginCtx.Set("accessProvider", "other-plugin")
			}
			if got := usage.APIKeyFromContext(ctx); got != wantKey {
				t.Fatalf("execution API key=%q, want authenticated snapshot %q", got, wantKey)
			}
			if got := usage.AccessProviderFromContext(ctx); got != wantProvider {
				t.Fatalf("execution provider=%q, want %q", got, wantProvider)
			}
			if got, ok := usage.IsNativeKeyFromContext(ctx); !ok || got != wantNative {
				t.Fatalf("execution native flag=%t explicit=%t, want %t", got, ok, wantNative)
			}
			capture := newCapturePluginExecutorUsagePlugin("v824-metadata")
			registerUsagePluginForTest(t, t.Name(), capture)
			usage.PublishRecord(ctx, usage.Record{Provider: "v824-metadata", Model: "synthetic-model"})
			got := capture.waitRecord(t)
			if got.APIKey != wantKey || got.AccessProvider != wantProvider || got.IsNativeKey != wantNative {
				t.Fatalf("usage queue lost auth identity: %+v", got)
			}
		})
	}
}
