//go:build windows

package pluginhost

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"
	"unsafe"
)

func TestHostStatusUpgrade739WindowsCallbackRoundTrip(t *testing.T) {
	testStatusUpgrade739Callback(t, func(host *Host, method string, request []byte) ([]byte, error) {
		id := windowsHostCallbackID.Add(1)
		windowsHostCallbackEntries.Store(id, dynamicHostCallbackEntry{host: host, pluginID: "status-callback"})
		defer windowsHostCallbackEntries.Delete(id)
		methodBytes := append([]byte(method), 0)
		var response windowsBuffer
		result := windowsHostCall(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&methodBytes[0])), uintptr(unsafe.Pointer(&request[0])), uintptr(len(request)), uintptr(unsafe.Pointer(&response)))
		runtime.KeepAlive(&id)
		runtime.KeepAlive(methodBytes)
		runtime.KeepAlive(request)
		if result != 0 {
			return nil, fmt.Errorf("windowsHostCall returned %d", result)
		}
		defer windowsHostFree(response.ptr, response.len)
		return bytes.Clone(unsafe.Slice((*byte)(unsafe.Pointer(response.ptr)), response.len)), nil
	})
}
