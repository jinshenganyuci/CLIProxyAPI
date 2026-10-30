package claude

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"
)

func TestUpgrade848ToolAdditionCacheDetachesRequestBacking(t *testing.T) {
	a, b := "first_declared_tool", "second_added_tool"
	input := []byte(fmt.Sprintf(`{"tools":[{"name":%q}],"messages":[{"role":"user","content":%q},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":%q}}}]}]}`, a, strings.Repeat("history-", 131072), b))
	p := &ConvertCodexResponseToClaudeParams{}
	if p.resolveToolUseName(input, a) != a || p.resolveToolUseName(input, b) != b {
		t.Fatal("mapping changed")
	}
	expected := strings.Index(string(input), b) - strings.Index(string(input), a)
	av, bv := p.ShortToOriginalToolNames[a], p.ShortToOriginalToolNames[b]
	delta := int64(uintptr(unsafe.Pointer(unsafe.StringData(bv)))) - int64(uintptr(unsafe.Pointer(unsafe.StringData(av))))
	t.Logf("retained_name_address_distance=%d serialized_name_distance=%d", delta, expected)
	if delta == int64(expected) {
		t.Fatal("stream cache names still share the whole request backing allocation")
	}
}
