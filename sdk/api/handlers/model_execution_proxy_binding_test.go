package handlers

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestExecuteModelProxyConflictCannotExecuteOrChangeOuterProvider(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, pinned := range []bool{false, true} {
			for _, bound := range []string{"direct", "http://credential.example:8080"} {
				t.Run(fmt.Sprintf("stream=%t/pinned=%t/%s", stream, pinned, bound), func(t *testing.T) {
					const model = "model-execution-bound-proxy"
					executions := 0
					executor := &modelExecutionCaptureExecutor{
						execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
							executions++
							return coreexecutor.Response{}, nil
						},
						stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
							executions++
							return nil, nil
						},
					}
					handler := newModelExecutionHandler(t, model, executor, &sdkconfig.SDKConfig{})
					id := "model-execution-" + model
					auth, _ := handler.AuthManager.GetByID(id)
					auth.ProxyURL = bound
					if _, err := handler.AuthManager.Update(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
					outerSelections := 0
					ctx := WithSelectedAuthProviderCallback(context.Background(), func(string) { outerSelections++ })
					req := ModelExecutionRequest{EntryProtocol: "openai", ExitProtocol: "openai", Model: model, Body: []byte(`{"model":"model-execution-bound-proxy"}`), ProxyURL: "http://different.example:8081", Stream: stream}
					if pinned {
						req.AuthID = id
					}
					if stream {
						_, err := handler.ExecuteModelStream(ctx, req)
						if err == nil || err.StatusCode != http.StatusBadRequest {
							t.Fatalf("stream error = %+v, want 400", err)
						}
					} else {
						_, err := handler.ExecuteModel(ctx, req)
						if err == nil || err.StatusCode != http.StatusBadRequest {
							t.Fatalf("error = %+v, want 400", err)
						}
					}
					if executions != 0 || outerSelections != 0 {
						t.Fatalf("conflict executed=%d or changed outer provider=%d", executions, outerSelections)
					}
					current, _ := handler.AuthManager.GetByID(id)
					if current.ProxyURL != bound || current.Failed != 0 || current.Quota.Exceeded || current.Unavailable {
						t.Fatalf("conflict mutated credential: %+v", current)
					}
					if active, limit := handler.AuthManager.CredentialConcurrencySnapshot(id); active != 0 || limit != 10 {
						t.Fatalf("concurrency %d/%d, want 0/10", active, limit)
					}
				})
			}
		}
	}
}
