package helps

import (
	"reflect"
	"testing"
)

func TestDevinObservedUsageMatchesCompletedInteractions(t *testing.T) {
	observed := ParseDevinUsage(&DevinUsage{PromptTokens: 3, CompletionTokens: 10, CachedTokens: 50, CacheWriteTokens: 14361})
	completed := ParseInteractionsUsage([]byte(`{"usage":{"total_input_tokens":53,"total_output_tokens":10,"total_cached_tokens":50,"cache_write_tokens":14361,"total_tokens":63}}`))
	if !reflect.DeepEqual(observed, completed) {
		t.Fatalf("observed usage changed completed semantics: got %+v, want %+v", observed, completed)
	}
	if detail := ParseDevinUsage(nil); detail.TotalTokens != 0 {
		t.Fatalf("missing usage manufactured tokens: %+v", detail)
	}
}
