package credentialconcurrency

import (
	"encoding/json"
	"math"
	"testing"
)

func TestParseValueValidIntegers(t *testing.T) {
	for _, value := range []any{int(3), int8(3), int16(3), int32(3), int64(3), uint(3), uint8(3), uint16(3), uint32(3), uint64(3), float32(3), float64(3), json.Number("3"), " 3 "} {
		got, errParse := ParseValue(value)
		if errParse != nil || got != 3 {
			t.Fatalf("ParseValue(%T(%v)) = %d, %v", value, value, got, errParse)
		}
	}
	for _, value := range []any{1, MaxLimit, uint64(MaxLimit), float64(MaxLimit)} {
		if _, errParse := ParseValue(value); errParse != nil {
			t.Fatalf("ParseValue(%v): %v", value, errParse)
		}
	}
}

func TestParseValueRejectsInvalidAndOverflowingValues(t *testing.T) {
	for _, value := range []any{nil, true, false, "", " ", "3.0", "1e3", "no", "9223372036854775808", json.Number("3.0"), json.Number("1e3"), json.Number("-1"), 0, -1, MaxLimit + 1, uint64(math.MaxUint64), uint(0), uint8(0), uint16(0), uint32(MaxLimit + 1), int64(math.MinInt64), float64(3.2), float32(0.5), math.Inf(1), math.Inf(-1), math.NaN(), math.MaxFloat64, []any{3}, map[string]any{"limit": 3}} {
		if _, errParse := ParseValue(value); errParse == nil {
			t.Fatalf("ParseValue(%T(%v)) accepted an invalid explicit limit", value, value)
		}
	}
}
