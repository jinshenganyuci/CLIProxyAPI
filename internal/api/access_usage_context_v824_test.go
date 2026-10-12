package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type usageContextV824Provider struct{ name string }

func (p usageContextV824Provider) Identifier() string { return p.name }
func (p usageContextV824Provider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return &sdkaccess.Result{Provider: p.name, Principal: "synthetic-client-key", Metadata: map[string]string{"auth_ids": "synthetic-bound-credential"}}, nil
}

func TestV824AccessMiddlewarePreservesPluginIdentityInUsageContext(t *testing.T) {
	for _, tc := range []struct {
		provider string
		native   bool
	}{
		{"config-inline", true},
		{"config-api-key", true},
		{"cpa-access-guard", false},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			manager := sdkaccess.NewManager()
			manager.SetProviders([]sdkaccess.Provider{usageContextV824Provider{name: tc.provider}})
			router := gin.New()
			router.Use(AuthMiddleware(manager))
			router.GET("/context", func(c *gin.Context) {
				ctx := c.Request.Context()
				if coreusage.APIKeyFromContext(ctx) != "synthetic-client-key" || coreusage.AccessProviderFromContext(ctx) != tc.provider {
					t.Error("authenticated usage identity was not forwarded to request context")
				}
				if native, present := coreusage.IsNativeKeyFromContext(ctx); !present || native != tc.native {
					t.Error("plugin key was classified as a native configuration key")
				}
				metadata, present := c.Get("accessMetadata")
				if !present || metadata.(map[string]string)["auth_ids"] != "synthetic-bound-credential" {
					t.Error("authentication metadata was lost")
				}
				c.Status(http.StatusNoContent)
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/context", nil))
			if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
				t.Fatal("middleware exposed internal identity in its response")
			}
		})
	}
}
