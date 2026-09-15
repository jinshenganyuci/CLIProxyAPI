package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAPICallMissingExplicitAuthNeverFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name, field, route          string
		deleted, noManager, literal bool
	}{
		{name: "snake_global", field: "auth_index", route: "global"},
		{name: "camel_direct", field: "authIndex", route: "direct"},
		{name: "pascal_request_proxy", field: "AuthIndex", route: "request"},
		{name: "deleted_devin_ui_quota", field: "authIndex", route: "global", deleted: true},
		{name: "missing_manager", field: "auth_index", route: "global", noManager: true},
		{name: "literal_payload", field: "auth_index", route: "global", literal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var directCalls, globalCalls, requestCalls atomic.Int32
			server := func(counter *atomic.Int32) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					counter.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"ok":true}`))
				}))
			}
			destination := server(&directCalls)
			defer destination.Close()
			global := server(&globalCalls)
			defer global.Close()
			requestProxy := server(&requestCalls)
			defer requestProxy.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			index := "missing-credential"
			if tc.deleted {
				auth, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
					ID: "deleted-devin.json", Provider: "devin", ProxyURL: requestProxy.URL,
					Metadata: map[string]any{"type": "devin", "api_key": "synthetic-key"},
				})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				index = auth.EnsureIndex()
				manager.Remove(context.Background(), auth.ID)
			}
			cfg := &config.Config{}
			cfg.ProxyURL = global.URL
			h := &Handler{cfg: cfg, authManager: manager}
			if tc.noManager {
				h.authManager = nil
			}
			payload := `{"metadata":{"apiKey":"$TOKEN$"}}`
			if tc.literal {
				payload = `{"ping":true}`
			}
			body := map[string]any{tc.field: index, "method": "POST", "url": destination.URL + "/GetUserStatus", "data": payload}
			switch tc.route {
			case "direct":
				body["proxy_url"] = "direct"
			case "request":
				body["proxy_url"] = requestProxy.URL
			}
			encoded, errMarshal := json.Marshal(body)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			router := gin.New()
			router.POST("/api-call", h.APICall)
			req := httptest.NewRequest(http.MethodPost, "/api-call", bytes.NewReader(encoded))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusNotFound {
				t.Errorf("status=%d, want=404: %s", recorder.Code, recorder.Body.String())
			}
			if directCalls.Load()+globalCalls.Load()+requestCalls.Load() != 0 {
				t.Errorf("unresolved credential escaped to network: direct=%d global=%d request=%d", directCalls.Load(), globalCalls.Load(), requestCalls.Load())
			}
		})
	}
}

func TestAPICallOmittedOrEmptyAuthStillAllowsGenericRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	router := gin.New()
	router.POST("/api-call", (&Handler{cfg: &config.Config{}}).APICall)
	for _, field := range []string{"", "auth_index", "authIndex", "AuthIndex"} {
		body := map[string]any{"method": "GET", "url": server.URL, "proxy_url": "direct"}
		if field != "" {
			body[field] = "  "
		}
		encoded, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		req := httptest.NewRequest(http.MethodPost, "/api-call", bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("field=%q status=%d", field, recorder.Code)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("generic requests=%d, want=4", calls.Load())
	}
}
