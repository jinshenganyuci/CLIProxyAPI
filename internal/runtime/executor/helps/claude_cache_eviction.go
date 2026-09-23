package helps

import "github.com/tidwall/gjson"

// ClaudePayloadHasCacheEviction checks protocol cache controls, excluding user
// text, tool inputs, schema properties, and other arbitrary data.
func ClaudePayloadHasCacheEviction(payload []byte) bool {
	root := gjson.ParseBytes(payload)
	hasEviction := func(block gjson.Result) bool {
		control := block.Get("cache_control")
		return control.IsObject() && control.Get("evict_on_complete").Exists()
	}
	if hasEviction(root) {
		return true
	}
	for _, field := range []string{"system", "tools"} {
		for _, block := range root.Get(field).Array() {
			if hasEviction(block) {
				return true
			}
		}
	}
	for _, message := range root.Get("messages").Array() {
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
