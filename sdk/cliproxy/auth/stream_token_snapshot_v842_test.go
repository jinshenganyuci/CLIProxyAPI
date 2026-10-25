package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type tokenSnapshotHookV842 struct {
	NoopHook
	results chan Result
}

func (h *tokenSnapshotHookV842) OnResult(_ context.Context, r Result) { h.results <- r }

func TestV842StreamResultCapturesTokenBeforeRequestReuse(t *testing.T) {
	for _, partition := range []string{"outer", "siwc", "empty"} {
		for _, outcome := range []string{"success", "401"} {
			t.Run(partition+"/"+outcome, func(t *testing.T) {
				hook := &tokenSnapshotHookV842{results: make(chan Result, 1)}
				m := NewManager(nil, nil, hook)
				a, err := m.Register(t.Context(), &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "outer-token", "use_siwc": true, "siwc": map[string]any{"access_token": "siwc-token"}}})
				if err != nil {
					t.Fatal(err)
				}
				if partition == "empty" {
					a.Metadata["siwc"].(map[string]any)["access_token"] = ""
					a, err = m.Update(t.Context(), a)
					if err != nil {
						t.Fatal(err)
					}
				}
				gc, _ := gin.CreateTestContext(httptest.NewRecorder())
				gc.Request = httptest.NewRequest("POST", "/v1/responses", nil)
				headers := http.Header{}
				want := "siwc-token"
				if partition == "outer" {
					gc.Request.Header.Set("User-Agent", "codex_cli_rs/0.154.0")
					want = "outer-token"
				}
				if partition == "empty" {
					want = ""
				}
				ctx := context.WithValue(t.Context(), "gin", gc)
				lease, err := m.acquireCredentialConcurrency(ctx, a)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Release()
				ctx = context.WithValue(ctx, credentialConcurrencyLeaseContextKey{}, lease)
				remaining := make(chan cliproxyexecutor.StreamChunk, 1)
				result := m.wrapStreamResult(ctx, a, "codex", "m", "m", nil, nil, remaining, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{Headers: headers}, m.nextExecutionSeq())
				// Gin may recycle the request after the handler returns. This channel
				// orders reuse before completion, exposing late lookups without a sleep.
				gc.Request = httptest.NewRequest("POST", "/v1/responses", nil)
				if partition != "outer" {
					gc.Request.Header.Set("User-Agent", "codex_cli_rs/0.154.0")
					headers.Set("User-Agent", "codex_cli_rs/0.154.0")
				}
				if outcome == "401" {
					remaining <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: 401, Message: "synthetic rejected token"}}
				}
				close(remaining)
				for range result.Chunks {
				}
				recorded := <-hook.results
				if recorded.AccessToken != want {
					t.Fatalf("completed stream token=%q, want start-of-stream %q", recorded.AccessToken, want)
				}
				got, _ := m.GetByID(a.ID)
				if outcome == "401" {
					if got.RejectedAccessToken != want {
						t.Fatalf("rejected token=%q want%q", got.RejectedAccessToken, want)
					}
					if partition != "siwc" && got.RejectedSIWCAccessToken != "" {
						t.Fatal("outer or empty execution rejected SIWC token")
					}
				}
				requireCredentialConcurrencySnapshot(t, m, a.ID, 0, 10)
			})
		}
	}
}
