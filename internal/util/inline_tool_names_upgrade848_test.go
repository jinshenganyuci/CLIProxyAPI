package util

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"
)

// Two retained names separated by a large message have the exact serialized
// address distance only when they still share that message backing allocation.
func TestUpgrade848InlineToolNamesDetachMessageBacking(t *testing.T) {
	a, b := "FirstAddedTool", "SecondAddedTool"
	input := []byte(fmt.Sprintf(`{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":%q}}}]},{"role":"user","content":%q},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":%q}}}]}]}`, a, strings.Repeat("history-", 131072), b))
	expected := strings.Index(string(input), b) - strings.Index(string(input), a)
	for _, rejectAmbiguous := range []bool{false, true} {
		t.Run(fmt.Sprint(rejectAmbiguous), func(t *testing.T) {
			names := toolNameMapFromClaudeRequest(input, rejectAmbiguous)
			av, bv := names[CanonicalToolName(a)], names[CanonicalToolName(b)]
			if av != a || bv != b {
				t.Fatal("inline name mapping changed")
			}
			delta := int64(uintptr(unsafe.Pointer(unsafe.StringData(bv)))) - int64(uintptr(unsafe.Pointer(unsafe.StringData(av))))
			t.Logf("retained_name_address_distance=%d serialized_name_distance=%d", delta, expected)
			if delta == int64(expected) {
				t.Fatal("inline names still share the whole messages backing allocation")
			}
		})
	}
}
