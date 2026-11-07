package chat_completions

import (
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

func TestUpgrade842CustomPatchCacheDoesNotPinRequest(t *testing.T) {
	original := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}],"messages":[{"role":"user","content":"` + strings.Repeat("unrelated-history-", 8192) + `"}]}`)
	var metadata codexChatToolMetadata
	metadata.resolveCustomPatchWinners(original)
	if !metadata.customPatchWinners["apply_patch"] {
		t.Fatal("custom patch winner was lost")
	}
	start := uintptr(unsafe.Pointer(unsafe.SliceData(original)))
	end := start + uintptr(len(original))
	for name := range metadata.customPatchWinners {
		pointer := uintptr(unsafe.Pointer(unsafe.StringData(name)))
		if pointer >= start && pointer < end {
			t.Fatal("cached custom tool name retains the complete parsed request")
		}
	}
	runtime.KeepAlive(original)
}
