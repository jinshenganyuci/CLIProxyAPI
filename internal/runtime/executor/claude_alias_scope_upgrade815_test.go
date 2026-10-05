package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestClaudeAliasContinuationIsolationUpgrade815(t *testing.T) {
	executor := &ClaudeExecutor{}
	create := []byte(`{"thread":{"type":"create"}}`)
	continued := []byte(`{"thread":{"type":"continue","previous_message_id":"same-message"}}`)
	auth := &coreauth.Auth{ID: "credential-a", Provider: "claude", ProxyURL: "http://proxy-a.invalid", Metadata: map[string]any{"access_token": "synthetic-old"}, Attributes: map[string]string{"base_url": "https://provider.invalid"}}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller-a"}}
	scope := helps.ClaudeToolAliasStateScope(context.Background(), auth, cliproxyexecutor.Request{}, opts)
	if scope == "" || strings.Contains(scope, "caller") || strings.Contains(scope, "credential") {
		t.Fatal("scope was missing or exposed identity material")
	}
	executor.rememberClaudeOAuthToolAliases(scope, create, map[string]string{"alias": "Read"}, "same-message")
	for _, mode := range []string{"other-credential", "other-caller", "other-upstream"} {
		t.Run(mode, func(t *testing.T) {
			other := auth.Clone()
			otherOpts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller-a"}}
			switch mode {
			case "other-credential":
				other.ID = "credential-b"
			case "other-caller":
				otherOpts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = "caller-b"
			case "other-upstream":
				other.Attributes["base_url"] = "https://different.invalid"
			}
			otherScope := helps.ClaudeToolAliasStateScope(context.Background(), other, cliproxyexecutor.Request{}, otherOpts)
			if otherScope == scope {
				t.Fatal("different owner reused continuation namespace")
			}
			if _, _, err := executor.prepareClaudeOAuthToolNamesForRequest(otherScope, continued, claudeMCPAliasOptions{}); err == nil {
				t.Fatal("continuation borrowed another owner alias state")
			}
			executor.rememberClaudeOAuthToolAliases(otherScope, create, map[string]string{"alias": "Write"}, "same-message")
			_, aliases, err := executor.prepareClaudeOAuthToolNamesForRequest(scope, continued, claudeMCPAliasOptions{})
			if err != nil || aliases["alias"] != "Read" {
				t.Fatal("same message ID replaced another owner's alias state")
			}
		})
	}
	rotated := auth.Clone()
	rotated.ProxyURL = "direct"
	rotated.Metadata["access_token"] = "synthetic-rotated"
	if helps.ClaudeToolAliasStateScope(context.Background(), rotated, cliproxyexecutor.Request{}, opts) != scope {
		t.Fatal("normal token refresh or proxy change lost registered credential scope")
	}
	if got := helps.ClaudeToolAliasStateScope(context.Background(), nil, cliproxyexecutor.Request{}, opts); got != "" {
		t.Fatal("missing credential acquired reusable scope")
	}
	requestOpts := cliproxyexecutor.Options{Headers: http.Header{"Authorization": []string{"header-caller"}}}
	request := cliproxyexecutor.Request{Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller-a"}}
	if helps.ClaudeToolAliasStateScope(context.Background(), auth, request, requestOpts) != scope {
		t.Fatal("explicit request caller scope did not precede header fallback")
	}
}

func TestClaudeAPIKeyViewsShareContinuationStoreUpgrade815(t *testing.T) {
	executor := &ClaudeExecutor{}
	first := executor.ForAPIKey().(*ClaudeExecutor)
	second := executor.ForAPIKey().(*ClaudeExecutor)
	if first == executor || second == first || first.claudeOAuthToolAliasStore() != second.claudeOAuthToolAliasStore() {
		t.Fatal("credential-specific views did not retain the shared continuation store")
	}
	first.rememberClaudeOAuthToolAliases("caller-and-credential", []byte(`{"thread":{"type":"create"}}`), map[string]string{"alias": "Read"}, "message")
	_, aliases, err := second.prepareClaudeOAuthToolNamesForRequest("caller-and-credential", []byte(`{"thread":{"type":"continue","previous_message_id":"message"}}`), claudeMCPAliasOptions{})
	if err != nil || aliases["alias"] != "Read" {
		t.Fatal("next API-key view lost successful continuation state")
	}
}
