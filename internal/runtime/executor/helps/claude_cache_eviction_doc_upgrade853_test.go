package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestV853ClaudeCacheEvictionDocObservesStagedProtocolFields(t *testing.T) {
	if ClaudePayloadHasCacheEvictionDoc(nil) {
		t.Fatal("nil document reported cache eviction")
	}
	for _, path := range []string{
		"cache_control.evict_on_complete",
		"system.0.cache_control.evict_on_complete",
		"tools.0.cache_control.evict_on_complete",
		"messages.0.content.0.cache_control.evict_on_complete",
		"messages.0.content.1.content.0.cache_control.evict_on_complete",
	} {
		t.Run(path, func(t *testing.T) {
			doc := ojson.NewDocString(`{"precision":9007199254740993,"system":[{"type":"text"}],"tools":[{"name":"tool"}],"messages":[{"content":[{"type":"text"},{"type":"tool_result","content":[{"type":"text"}]}]}]}`)
			if ClaudePayloadHasCacheEvictionDoc(doc) {
				t.Fatal("unmodified document reported cache eviction")
			}
			if errSet := doc.Set(path, false); errSet != nil {
				t.Fatal(errSet)
			}
			if !ClaudePayloadHasCacheEvictionDoc(doc) {
				t.Fatal("staged cache eviction field was ignored")
			}
			if !ClaudePayloadHasCacheEviction(doc.Bytes()) || doc.Get("precision").Raw != "9007199254740993" {
				t.Fatal("document observer disagreed with bytes or lost precision")
			}
			if errDelete := doc.Delete(path); errDelete != nil {
				t.Fatal(errDelete)
			}
			if ClaudePayloadHasCacheEvictionDoc(doc) {
				t.Fatal("deleted cache eviction field remained visible")
			}
		})
	}
	for _, payload := range []string{
		`{"messages":[{"content":[{"type":"tool_use","input":{"cache_control":{"evict_on_complete":true}}}]}]}`,
		`{"tools":[{"input_schema":{"cache_control":{"evict_on_complete":true}}}]}`,
		`{"metadata":{"cache_control":{"evict_on_complete":true}}}`,
	} {
		if ClaudePayloadHasCacheEvictionDoc(ojson.NewDocString(payload)) || ClaudePayloadHasCacheEviction([]byte(payload)) {
			t.Fatalf("arbitrary user data treated as protocol cache control: %s", payload)
		}
	}
}
