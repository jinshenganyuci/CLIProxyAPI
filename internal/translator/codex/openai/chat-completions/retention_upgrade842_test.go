package chat_completions

import (
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/tidwall/gjson"
)

func TestUpgrade842CustomPatchCacheDoesNotPinRequest(t *testing.T) {
	root := gjson.Parse(`{"tools":[{"type":"custom","name":"apply_patch"}],"messages":[{"role":"user","content":"` + strings.Repeat("unrelated-history-", 8192) + `"}]}`)
	var metadata codexChatToolMetadata
	metadata.resolveCustomPatchWinners(root)
	if !metadata.customPatchWinners["apply_patch"] {
		t.Fatal("custom patch winner was lost")
	}
	start := uintptr(unsafe.Pointer(unsafe.StringData(root.Raw)))
	end := start + uintptr(len(root.Raw))
	for name := range metadata.customPatchWinners {
		pointer := uintptr(unsafe.Pointer(unsafe.StringData(name)))
		if pointer >= start && pointer < end {
			t.Fatal("cached custom tool name retains the complete parsed request")
		}
	}
	runtime.KeepAlive(root)
}
