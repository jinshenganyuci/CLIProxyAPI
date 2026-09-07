package management

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	log "github.com/sirupsen/logrus"
)

func reportCodexOAuthExchangeError(state string, err error) {
	summary := codexOAuthExchangeErrorSummary(err)
	SetOAuthSessionError(state, summary)
	log.WithField("provider", "codex").Warn(summary)
}

// Exchange errors may embed proxy credentials, authorization codes, or token
// endpoint bodies. Only fixed categories and a validated HTTP status are safe
// to expose in polling responses and logs.
func codexOAuthExchangeErrorSummary(err error) string {
	const prefix = "Failed to exchange authorization code for tokens"
	if errors.Is(err, context.Canceled) {
		return prefix + ": request cancelled"
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return prefix + ": request timed out"
	}
	if err != nil {
		if _, status, found := strings.Cut(err.Error(), "token exchange failed with status "); found && len(status) >= 3 {
			if status[0] >= '1' && status[0] <= '5' && status[1] >= '0' && status[1] <= '9' && status[2] >= '0' && status[2] <= '9' {
				return fmt.Sprintf("%s: token endpoint returned HTTP %s", prefix, status[:3])
			}
		}
	}
	return prefix + ": request failed"
}
