package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAPICallBodyTokenUsesReloadedCredentialAndSelectedProxy(t *testing.T) {
	for _, tc := range []struct {
		name, field, token, selected, override, route string
		status                                        int
	}{
		{name: "api_key", field: "api_key", token: "synthetic-api-key", selected: "credential", route: "credential"},
		{name: "session_token", field: "session_token", token: "synthetic-session-token", selected: "credential", route: "credential"},
		{name: "json_quotes", field: "api_key", token: "synthetic-\"quote\\backslash", selected: "credential", route: "credential"},
		{name: "json_newline", field: "session_token", token: "synthetic-newline\nnext\tpart", selected: "credential", route: "credential"},
		{name: "explicit_direct", field: "api_key", token: "synthetic-direct", selected: "direct", route: "direct"},
		{name: "same_override", field: "api_key", token: "synthetic-same", selected: "credential", override: "credential", route: "credential"},
		{name: "reject_override", field: "api_key", token: "synthetic-override", selected: "credential", override: "direct", status: http.StatusConflict},
		{name: "invalid_before_token_resolution", selected: "invalid-proxy", status: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counters := map[string]*atomic.Int32{"direct": {}, "credential": {}, "global": {}}
			serve := func(route string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					counters[route].Add(1)
					var body struct {
						Metadata struct {
							APIKey string `json:"apiKey"`
						} `json:"metadata"`
						Repeated string `json:"repeated"`
					}
					if errDecode := json.NewDecoder(r.Body).Decode(&body); errDecode != nil {
						t.Errorf("body token substitution broke JSON: %v", errDecode)
					} else if body.Metadata.APIKey != tc.token || body.Repeated != tc.token+"/"+tc.token {
						t.Error("body token did not come from the selected canonical credential")
					}
					if !strings.ContainsAny(tc.token, "\r\n\t") && r.Header.Get("Authorization") != "Bearer "+tc.token {
						t.Error("header and body used different credential tokens")
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"ok":true}`))
				}
			}
			destination := httptest.NewServer(serve("direct"))
			defer destination.Close()
			credential := httptest.NewServer(serve("credential"))
			defer credential.Close()
			global := httptest.NewServer(serve("global"))
			defer global.Close()
			resolve := func(value string) string {
				if value == "credential" {
					return credential.URL
				}
				return value
			}
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(t.TempDir())
			metadata := map[string]any{"type": "devin", "auth_kind": "oauth", "proxy_url": resolve(tc.selected)}
			if tc.field != "" {
				metadata[tc.field] = tc.token
			}
			if _, errSave := store.Save(context.Background(), &coreauth.Auth{ID: "devin-body.json", FileName: "devin-body.json", Provider: "devin", Metadata: metadata}); errSave != nil {
				t.Fatal(errSave)
			}
			loaded, errList := store.List(context.Background())
			if errList != nil || len(loaded) != 1 {
				t.Fatalf("canonical reload failed: count=%d error=%v", len(loaded), errList)
			}
			if loaded[0].Attributes["api_key"] != "" || loaded[0].Attributes["session_token"] != "" {
				t.Fatal("test requires a reloaded credential with tokens only in metadata")
			}
			manager := coreauth.NewManager(nil, nil, nil)
			auth, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), loaded[0])
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			cfg := &config.Config{}
			cfg.ProxyURL = global.URL
			h := &Handler{cfg: cfg, authManager: manager}
			headers := map[string]string{"Content-Type": "application/json"}
			if !strings.ContainsAny(tc.token, "\r\n\t") {
				headers["Authorization"] = "Bearer $TOKEN$"
			}
			data, errMarshal := json.Marshal(map[string]any{
				"auth_index": auth.EnsureIndex(), "url": destination.URL + "/body-probe", "method": "POST",
				"proxy_url": resolve(tc.override), "header": headers,
				"data": `{"metadata":{"apiKey":"$TOKEN$"},"repeated":"$TOKEN$/$TOKEN$"}`,
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			router := gin.New()
			router.POST("/api-call", h.APICall)
			request := httptest.NewRequest(http.MethodPost, "/api-call", bytes.NewReader(data))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			wantStatus := tc.status
			if wantStatus == 0 {
				wantStatus = http.StatusOK
			}
			if recorder.Code != wantStatus {
				t.Fatalf("status=%d, want=%d: %s", recorder.Code, wantStatus, recorder.Body.String())
			}
			for route, counter := range counters {
				var want int32
				if route == tc.route {
					want = 1
				}
				if counter.Load() != want {
					t.Errorf("%s received %d requests, want %d", route, counter.Load(), want)
				}
			}
		})
	}
}
