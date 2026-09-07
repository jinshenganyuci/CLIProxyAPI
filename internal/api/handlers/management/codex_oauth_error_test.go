package management

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestCodexOAuthExchangeErrorsAreSanitizedInStatusAndLogs(t *testing.T) {
	previousHooks := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	previousLevel := log.GetLevel()
	log.SetLevel(log.WarnLevel)
	capture := logtest.NewGlobal()
	t.Cleanup(func() {
		log.StandardLogger().ReplaceHooks(previousHooks)
		log.SetLevel(previousLevel)
	})
	for _, testCase := range []struct {
		name     string
		failure  error
		expected string
	}{
		{"network", &url.Error{Op: "Post", URL: "https://auth.openai.com/oauth/token?code=synthetic-secret-code", Err: fmt.Errorf("proxy http://user:synthetic-secret-password@proxy.invalid rejected request")}, "request failed"},
		{"upstream_body", fmt.Errorf("token exchange failed with status 400: proxy=synthetic-secret-password code=synthetic-secret-code"), "HTTP 400"},
		{"timeout", fmt.Errorf("proxy synthetic-secret-password: %w", context.DeadlineExceeded), "timed out"},
		{"cancel", fmt.Errorf("code synthetic-secret-code: %w", context.Canceled), "cancelled"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			state := codex.NewCredentialIdentityNamespace()
			RegisterOAuthSession(state, "codex")
			t.Cleanup(func() { CancelOAuthSession(state) })
			reportCodexOAuthExchangeError(state, testCase.failure)
			_, status, exists := GetOAuthSession(state)
			if !exists || !strings.Contains(status, testCase.expected) {
				t.Fatal("polling status lost the safe error category")
			}
			var entry *log.Entry
			for _, candidate := range capture.AllEntries() {
				if candidate.Message == status {
					entry = candidate
				}
			}
			if entry == nil {
				t.Fatal("logged summary differed from safe polling status")
			}
			for _, secret := range []string{"synthetic-secret-password", "synthetic-secret-code", "user:", "?code="} {
				if strings.Contains(status, secret) || strings.Contains(entry.Message, secret) || strings.Contains(fmt.Sprint(entry.Data), secret) {
					t.Fatal("OAuth exchange error exposed secret material")
				}
			}
		})
	}
}
