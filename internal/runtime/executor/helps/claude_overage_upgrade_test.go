package helps

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestClaudeUpgradeOverageResetScopeAndDeadlines(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, scenario := range []struct {
		name        string
		headers     http.Header
		wantUnified bool
		wantReset   time.Duration
	}{
		{
			name:      "missing-5h-with-healthy-utilization",
			headers:   http.Header{"Anthropic-Ratelimit-Unified-7d-Status": {"allowed_warning"}, "Anthropic-Ratelimit-Unified-5h-Utilization": {"0.00"}},
			wantReset: 120 * time.Second,
		},
		{
			name:      "missing-7d-with-healthy-utilization",
			headers:   http.Header{"Anthropic-Ratelimit-Unified-5h-Status": {"allowed"}, "Anthropic-Ratelimit-Unified-7d-Utilization": {"0.99"}},
			wantReset: 120 * time.Second,
		},
		{
			name:        "explicit-shared-rejection-overrides-healthy-utilization",
			headers:     http.Header{"Anthropic-Ratelimit-Unified-5h-Status": {"rejected"}, "Anthropic-Ratelimit-Unified-7d-Status": {"allowed"}, "Anthropic-Ratelimit-Unified-5h-Utilization": {"0.00"}},
			wantUnified: true,
			wantReset:   7 * 24 * time.Hour,
		},
		{
			name:        "missing-shared-health-remains-conservative",
			headers:     http.Header{"Anthropic-Ratelimit-Unified-7d-Status": {"allowed"}},
			wantUnified: true,
			wantReset:   7 * 24 * time.Hour,
		},
		{
			name:      "both-shared-windows-allowed",
			headers:   http.Header{"Anthropic-Ratelimit-Unified-5h-Status": {"allowed_warning"}, "Anthropic-Ratelimit-Unified-7d-Status": {"allowed"}},
			wantReset: 120 * time.Second,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			headers := scenario.headers.Clone()
			headers.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
			headers.Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
			headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
			headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
			headers.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
			headers.Set("Retry-After", "120")
			if got := ClaudeHeadersIndicateUnifiedRateLimitRejection(headers); got != scenario.wantUnified {
				t.Fatalf("credential rejection = %t, want %t", got, scenario.wantUnified)
			}
			reset := parseClaudeRateLimitResetWithFuzz(headers, now, 0, 0)
			if reset == nil || *reset != scenario.wantReset {
				t.Fatalf("cooldown reset = %v, want %v", reset, scenario.wantReset)
			}
		})
	}
}
