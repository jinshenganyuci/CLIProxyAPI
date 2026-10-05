package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestV815ClaudeContinuationAliasesIsolateCredentialAndCaller(t *testing.T) {
	const model = "claude-opus-5"
	var requests atomic.Int32
	var mu sync.Mutex
	aliases := make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Error(errRead)
			return
		}
		// Cloaking can prepend text blocks; the caller marker remains the last text.
		caller := ""
		content := gjson.GetBytes(body, "messages.0.content")
		if content.Type == gjson.String {
			caller = content.String()
		} else {
			for _, item := range content.Array() {
				if strings.HasPrefix(item.Get("text").String(), "caller-") {
					caller = item.Get("text").String()
				}
			}
		}
		key := r.Header.Get("Authorization") + "/" + caller
		mu.Lock()
		if gjson.GetBytes(body, "thread.type").String() == "create" {
			aliases[key] = gjson.GetBytes(body, "tools.0.name").String()
		}
		alias := aliases[key]
		mu.Unlock()
		if caller == "" || !strings.HasPrefix(alias, "mcp__") {
			t.Errorf("OAuth alias fixture was not prepared: caller=%q alias=%q", caller, alias)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"shared-message","type":"message","role":"assistant","model":%q,"content":[{"type":"tool_use","id":"shared-tool","name":%q,"input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":2}}`, model, alias)
	}))
	t.Cleanup(server.Close)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 0, 3)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
	for _, id := range []string{"credential-a", "credential-b"} {
		_, errRegister := manager.Register(t.Context(), &coreauth.Auth{
			ID: id, Provider: "claude", Status: coreauth.StatusActive, ProxyURL: "direct",
			Attributes: map[string]string{"api_key": "sk-ant-oat-synthetic-" + id, "base_url": server.URL},
			Metadata:   map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "claude_device_ids": []string{strings.Repeat("0", 64)}},
		})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	base := NewBaseAPIHandlers(&config.SDKConfig{}, manager)
	var completions []pluginapi.RequestCompletion
	base.SetPluginHost(&handlerInterceptorTestHost{completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) {
		completions = append(completions, completion)
	}})
	call := func(authID, caller, tool string, wantStatus int) {
		t.Helper()
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Set("userApiKey", caller)
		ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx := context.WithValue(t.Context(), "gin", ginCtx)
		payload := map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": caller}}, "max_tokens": 32}
		if tool != "" {
			payload["thread"] = map[string]any{"type": "create"}
			payload["tools"] = []any{map[string]any{"name": tool, "input_schema": map[string]any{"type": "object"}}}
		} else {
			payload["thread"] = map[string]any{"type": "continue", "previous_message_id": "shared-message"}
		}
		body, errEncode := json.Marshal(payload)
		if errEncode != nil {
			t.Fatal(errEncode)
		}
		before := requests.Load()
		response, errExecute := base.ExecuteModel(ctx, ModelExecutionRequest{Model: model, EntryProtocol: "claude", ExitProtocol: "claude", AuthID: authID, Body: body, Headers: http.Header{"Authorization": {"Bearer " + caller}}})
		if wantStatus == http.StatusNotFound {
			if errExecute == nil || errExecute.StatusCode != http.StatusNotFound || !strings.Contains(errExecute.Error.Error(), "previous_message_id") {
				t.Fatalf("foreign continuation did not fail locally: %+v", errExecute)
			}
			if requests.Load() != before {
				t.Fatal("unknown/foreign alias state reached an upstream credential")
			}
		} else {
			if errExecute != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("valid thread failed: %+v", errExecute)
			}
			wantTool := tool
			if wantTool == "" {
				switch authID + "/" + caller {
				case "credential-a/caller-a":
					wantTool = "ReadAlpha"
				case "credential-b/caller-a":
					wantTool = "WriteBeta"
				case "credential-a/caller-b":
					wantTool = "SearchGamma"
				}
			}
			if got := gjson.GetBytes(response.Body, "content.0.name").String(); got != wantTool {
				t.Fatalf("continuation alias crossed caller/credential scope: got %q want %q", got, wantTool)
			}
		}
		if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 0 || limit != 10 {
			t.Fatalf("thread success/rejection leaked default admission: %d/%d", active, limit)
		}
		current, _ := manager.GetByID(authID)
		if current.Unavailable || current.Status != coreauth.StatusActive || current.LastError != nil || current.Quota.Exceeded || !current.NextRetryAfter.IsZero() || current.ProxyURL != "direct" {
			t.Fatalf("request-scoped missing state damaged credential health: %#v", current)
		}
	}
	call("credential-a", "caller-a", "ReadAlpha", http.StatusOK)
	call("credential-b", "caller-a", "", http.StatusNotFound)
	call("credential-a", "caller-b", "", http.StatusNotFound)
	call("credential-b", "caller-a", "WriteBeta", http.StatusOK)
	call("credential-a", "caller-b", "SearchGamma", http.StatusOK)
	call("credential-a", "caller-a", "", http.StatusOK)
	call("credential-b", "caller-a", "", http.StatusOK)
	call("credential-a", "caller-b", "", http.StatusOK)
	if requests.Load() != 6 || len(completions) != 8 {
		t.Fatalf("request/lifecycle count = %d/%d, want 6/8", requests.Load(), len(completions))
	}
}
