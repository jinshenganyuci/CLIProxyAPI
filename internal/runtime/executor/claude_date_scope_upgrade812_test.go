package executor

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestClaudePinnedDateIsCredentialScopedAndDoesNotChangeRouteUpgrade812(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)
	oldClock := claudeCodeCurrentTimeFunc
	current := time.Date(2026, 10, 3, 23, 59, 0, 0, time.UTC)
	claudeCodeCurrentTimeFunc = func(*config.Config, *coreauth.Auth) time.Time { return current }
	t.Cleanup(func() { claudeCodeCurrentTimeFunc = oldClock })
	ctx := helps.WithClaudeSessionID(context.Background(), "same-downstream-session")
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}]}`)
	first := &coreauth.Auth{ID: "credential-a", ProxyURL: "http://first-proxy.invalid:8080"}
	_, _, anchor, ok := resolveClaudeContinuityTags(ctx, nil, first, http.Header{}, payload, true, "", "")
	if !ok || anchor.PinnedDate != "2026-10-03" {
		t.Fatal("first credential did not pin the local date")
	}
	current = current.Add(2 * time.Minute)
	first.ProxyURL = "socks5://second-proxy.invalid:1080"
	_, _, continued, ok := resolveClaudeContinuityTags(ctx, nil, first, http.Header{}, payload, true, "", "")
	if !ok || continued.Key != anchor.Key || continued.PinnedDate != anchor.PinnedDate || first.ProxyURL != "socks5://second-proxy.invalid:1080" {
		t.Fatal("session pin replaced route or changed across midnight")
	}
	second := &coreauth.Auth{ID: "credential-b", ProxyURL: "direct"}
	_, _, separate, ok := resolveClaudeContinuityTags(ctx, nil, second, http.Header{}, payload, true, "", "")
	if !ok || separate.Key == anchor.Key || separate.PinnedDate != "2026-10-04" || second.ProxyURL != "direct" {
		t.Fatal("date pin crossed credentials or changed explicit direct")
	}
}
