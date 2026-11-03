package helps

import "github.com/MoeForge/moego/ojson"

// ClaudePayloadHasCacheEviction checks protocol cache controls, excluding user
// text, tool inputs, schema properties, and other arbitrary data.
func ClaudePayloadHasCacheEviction(payload []byte) bool {
	return claudePayloadHasCacheEviction(ojson.ParseBytes(payload).Get)
}

// ClaudePayloadHasCacheEvictionDoc observes the current staged payload without serialization.
func ClaudePayloadHasCacheEvictionDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	return claudePayloadHasCacheEviction(doc.Get)
}

func claudePayloadHasCacheEviction(get func(string) ojson.Result) bool {
	hasEviction := func(block ojson.Result) bool {
		control := block.Get("cache_control")
		return control.IsObject() && control.Get("evict_on_complete").Exists()
	}
	control := get("cache_control")
	if control.IsObject() && control.Get("evict_on_complete").Exists() {
		return true
	}
	for _, field := range []string{"system", "tools"} {
		for _, block := range get(field).Array() {
			if hasEviction(block) {
				return true
			}
		}
	}
	for _, message := range get("messages").Array() {
		for _, block := range message.Get("content").Array() {
			if hasEviction(block) {
				return true
			}
			if block.Get("type").String() == "tool_result" {
				for _, resultBlock := range block.Get("content").Array() {
					if hasEviction(resultBlock) {
						return true
					}
				}
			}
		}
	}
	return false
}
