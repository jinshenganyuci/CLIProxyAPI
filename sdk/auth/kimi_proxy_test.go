package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestKimiAuthenticatorsRejectInvalidSelectedProxyBeforeLogin(t *testing.T) {
	for _, authenticator := range []Authenticator{NewKimiAuthenticator(), NewKimiAIAuthenticator(), NewKimiAIDotAuthenticator()} {
		for _, proxy := range []string{" ", "invalid://user:secret@proxy.invalid"} {
			record, errLogin := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{NoBrowser: true, Metadata: map[string]string{"proxy_url": proxy}})
			if record != nil || errLogin == nil || strings.Contains(errLogin.Error(), "secret") {
				t.Fatalf("%s accepted or exposed invalid proxy: %v", authenticator.Provider(), errLogin)
			}
		}
	}
}
