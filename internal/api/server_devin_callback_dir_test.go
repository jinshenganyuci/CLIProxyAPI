package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	management "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
)

func TestOAuthCallbacksHonorFrozenDevinAuthDir(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-frozen-callback-key")
	server := newTestServer(t)
	for index, test := range []struct {
		name, provider, publicPath string
		bound, cancel              bool
		status                     int
	}{
		{name: "public frozen Devin", provider: "devin", publicPath: "/callback", bound: true, status: http.StatusOK},
		{name: "legacy public frozen Devin", provider: "devin", publicPath: "/devin/callback", bound: true, status: http.StatusOK},
		{name: "management frozen Devin", provider: "devin", bound: true, status: http.StatusOK},
		{name: "legacy Devin follows current directory", provider: "devin", status: http.StatusOK},
		{name: "legacy Codex follows current directory", provider: "codex", status: http.StatusOK},
		{name: "cancelled public flow cannot write", provider: "devin", publicPath: "/callback", bound: true, cancel: true, status: http.StatusBadRequest},
		{name: "cancelled legacy public flow cannot write", provider: "devin", publicPath: "/devin/callback", bound: true, cancel: true, status: http.StatusBadRequest},
		{name: "cancelled management flow cannot write", provider: "devin", bound: true, cancel: true, status: http.StatusNotFound},
		{name: "public provider mismatch cannot write", provider: "codex", publicPath: "/callback", bound: true, status: http.StatusBadRequest},
		{name: "legacy public provider mismatch cannot write", provider: "codex", publicPath: "/devin/callback", bound: true, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := fmt.Sprintf("devin-frozen-callback-%d", index)
			frozenDir, currentDir := t.TempDir(), t.TempDir()
			before := server.cfg.CloneForRuntime()
			before.AuthDir = frozenDir
			server.cfg = before
			server.mgmt.SetConfig(before)
			if test.bound {
				management.RegisterOAuthSessionWithAuthDir(state, test.provider, before.AuthDir)
			} else {
				management.RegisterOAuthSession(state, test.provider)
			}
			defer management.CompleteOAuthSession(state)
			if test.cancel && !management.CancelOAuthSession(state) {
				t.Fatal("failed to cancel the pending session")
			}

			// Both real callback handlers now see a different config from login.
			after := before.CloneForRuntime()
			after.AuthDir = currentDir
			server.cfg = after
			server.mgmt.SetConfig(after)
			var request *http.Request
			if test.publicPath != "" {
				request = httptest.NewRequest(http.MethodGet, test.publicPath+"?state="+state+"&code=callback-code", nil)
			} else {
				body := fmt.Sprintf(`{"provider":%q,"state":%q,"code":"callback-code"}`, test.provider, state)
				request = httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer test-frozen-callback-key")
			}
			recorder := httptest.NewRecorder()
			server.engine.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("callback status = %d, want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
			for _, directory := range []string{frozenDir, currentDir} {
				entries, errRead := os.ReadDir(directory)
				if errRead != nil {
					t.Fatal(errRead)
				}
				wantFile := test.status == http.StatusOK && (test.bound && directory == frozenDir || !test.bound && directory == currentDir)
				if !wantFile {
					if len(entries) != 0 {
						t.Fatalf("callback wrote into an unexpected directory: %s", directory)
					}
					continue
				}
				if len(entries) != 1 {
					t.Fatalf("callback file count = %d, want 1", len(entries))
				}
				data, errReadFile := os.ReadFile(filepath.Join(directory, ".oauth-"+test.provider+"-"+state+".oauth"))
				if errReadFile != nil {
					t.Fatal(errReadFile)
				}
				var payload map[string]string
				if errDecode := json.Unmarshal(data, &payload); errDecode != nil {
					t.Fatal(errDecode)
				}
				if payload["state"] != state || payload["code"] != "callback-code" || payload["error"] != "" {
					t.Fatalf("unexpected callback payload: %v", payload)
				}
			}
		})
	}
}
