package codex

import (
	"encoding/json"
	"math/big"
	"reflect"
)

// MarshalJSONPreservingUnchanged retains existing JSON values that are unchanged
// in the ordinary map[string]any representation. This prevents a float64 decoded
// from disk from rounding an unrelated large number on the next save. Objects
// and arrays are visited recursively; omitted keys stay deleted and new values
// remain authoritative. Explicit json.Number, integer and json.RawMessage values
// are never replaced by a float64 comparison. This does not change runtime types
// or recover precision already lost before the existing file was written.
func MarshalJSONPreservingUnchanged(existing []byte, data map[string]any) ([]byte, error) {
	// Validate before recursive traversal, including unsupported values and cycles.
	raw, errMarshal := json.Marshal(data)
	if errMarshal != nil {
		return nil, errMarshal
	}
	var disk map[string]json.RawMessage
	if len(existing) == 0 || json.Unmarshal(existing, &disk) != nil || disk == nil {
		return raw, nil
	}
	return json.Marshal(preserveJSONObjectValues(disk, data))
}

func preserveJSONObjectValues(disk map[string]json.RawMessage, data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	out := make(map[string]any, len(data))
	for key, value := range data {
		old, exists := disk[key]
		if !exists {
			out[key] = value
			continue
		}
		out[key] = preserveJSONValue(old, value)
	}
	return out
}

func preserveJSONValue(existing json.RawMessage, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		var disk map[string]json.RawMessage
		if json.Unmarshal(existing, &disk) == nil {
			return preserveJSONObjectValues(disk, typed)
		}
	case []any:
		if typed == nil {
			return typed
		}
		var disk []json.RawMessage
		if json.Unmarshal(existing, &disk) == nil {
			out := make([]any, len(typed))
			for index, child := range typed {
				if index < len(disk) {
					out[index] = preserveJSONValue(disk[index], child)
				} else {
					out[index] = child
				}
			}
			return out
		}
	default:
		// Only values produced by normal JSON decoding can be stale float64
		// projections. Precise caller-provided numeric/raw types must take effect.
		switch value.(type) {
		case json.Number, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			if equalExactJSONNumber(existing, value) {
				return existing
			}
		case nil, bool, string, float64:
			var decoded any
			if json.Unmarshal(existing, &decoded) == nil && reflect.DeepEqual(decoded, value) {
				return existing
			}
		}
	}
	return value
}

// Exact numeric equality keeps existing formatting such as 3.0 for an integer
// control value of 3, without conflating adjacent integers above 2^53.
func equalExactJSONNumber(existing json.RawMessage, value any) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	before, okBefore := new(big.Rat).SetString(string(existing))
	after, okAfter := new(big.Rat).SetString(string(raw))
	return okBefore && okAfter && before.Cmp(after) == 0
}
