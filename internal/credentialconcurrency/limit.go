// Package credentialconcurrency validates per-credential local concurrency limits.
package credentialconcurrency

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	// Field is the canonical auth metadata and attribute key.
	Field = "concurrency_limit"
	// DefaultLimit applies when no explicit credential limit is configured.
	DefaultLimit = 10
	// MaxLimit bounds accepted configuration values.
	MaxLimit = 1_000_000
)

func validate(limit int64) (int, error) {
	if limit < 1 || limit > MaxLimit {
		return 0, fmt.Errorf("concurrency limit must be between 1 and %d", MaxLimit)
	}
	return int(limit), nil
}

// ParseString parses an explicit integer limit. Omission is handled by callers.
func ParseString(raw string) (int, error) {
	limit, errParse := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if errParse != nil {
		return 0, fmt.Errorf("concurrency limit must be an integer")
	}
	return validate(limit)
}

// ParseValue parses an explicit metadata value without truncation or overflow.
// Nil is deliberately rejected; callers decide whether null resets the limit.
func ParseValue(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return validate(int64(typed))
	case int8:
		return validate(int64(typed))
	case int16:
		return validate(int64(typed))
	case int32:
		return validate(int64(typed))
	case int64:
		return validate(typed)
	case uint:
		return parseUnsigned(uint64(typed))
	case uint8:
		return parseUnsigned(uint64(typed))
	case uint16:
		return parseUnsigned(uint64(typed))
	case uint32:
		return parseUnsigned(uint64(typed))
	case uint64:
		return parseUnsigned(typed)
	case float32:
		return ParseValue(float64(typed))
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed {
			return 0, fmt.Errorf("concurrency limit must be an integer")
		}
		if typed < 1 || typed > MaxLimit {
			return 0, fmt.Errorf("concurrency limit must be between 1 and %d", MaxLimit)
		}
		return int(typed), nil
	case json.Number:
		return ParseString(string(typed))
	case string:
		return ParseString(typed)
	default:
		return 0, fmt.Errorf("concurrency limit must be an integer")
	}
}

func parseUnsigned(limit uint64) (int, error) {
	if limit > MaxLimit {
		return 0, fmt.Errorf("concurrency limit must be between 1 and %d", MaxLimit)
	}
	return validate(int64(limit))
}
