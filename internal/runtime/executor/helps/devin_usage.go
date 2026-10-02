package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// ParseDevinUsage retains Connect-RPC usage before response translation can fail.
func ParseDevinUsage(source *DevinUsage) usage.Detail {
	if source == nil {
		return usage.Detail{}
	}
	input, validInput := safeUsageTokenSum(source.PromptTokens, source.CachedTokens)
	total, validTotal := safeUsageTokenSum(input, source.CompletionTokens)
	detail := usage.Detail{
		InputTokens: input, OutputTokens: source.CompletionTokens, TotalTokens: total,
		CachedTokens: source.CachedTokens, CacheReadTokens: source.CachedTokens,
		CacheCreationTokens: source.CacheWriteTokens,
	}
	if !validInput || !validTotal {
		detail.TokenBreakdown = invalidUsageTokenBreakdown(total)
		return detail
	}
	detail.TokenBreakdown = usage.NewSeparateReasoningTokenBreakdown(input, source.CachedTokens, source.CacheWriteTokens, source.CompletionTokens, 0, total)
	return detail
}
