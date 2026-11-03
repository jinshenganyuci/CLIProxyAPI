package common

import (
	"encoding/json"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// RetainedJSONValue detaches values from request storage without rounding numbers.
// Object values remain maps so normal serialization keeps stable key ordering.
func RetainedJSONValue(value ojson.Result) any {
	switch value.Type {
	case ojson.String:
		return strings.Clone(value.String())
	case ojson.Number:
		return json.Number(strings.Clone(value.Raw))
	case ojson.True:
		return true
	case ojson.False:
		return false
	case ojson.JSON:
		if value.IsArray() {
			items := make([]any, 0)
			value.ForEach(func(_, item ojson.Result) bool {
				items = append(items, RetainedJSONValue(item))
				return true
			})
			return items
		}
		if value.IsObject() {
			fields := make(map[string]any)
			value.ForEach(func(key, item ojson.Result) bool {
				fields[strings.Clone(key.String())] = RetainedJSONValue(item)
				return true
			})
			return fields
		}
	}
	return nil
}
