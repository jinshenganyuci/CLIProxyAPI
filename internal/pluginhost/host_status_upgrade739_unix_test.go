//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHostStatusUpgrade739NativeCallbackRoundTrip(t *testing.T) {
	compiler, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("C compiler unavailable for native callback fixture")
	}
	library := filepath.Join(t.TempDir(), "status-callback.so")
	args := []string{"-shared", "-fPIC", "-O0", "-o", library, filepath.Join("testdata", "status_callback_upgrade739.c")}
	if runtime.GOOS == "darwin" {
		args[0] = "-dynamiclib"
	}
	if out, errCompile := exec.Command(compiler, args...).CombinedOutput(); errCompile != nil {
		t.Fatalf("build native callback fixture: %v\n%s", errCompile, out)
	}
	testStatusUpgrade739Callback(t, func(host *Host, method string, request []byte) ([]byte, error) {
		client, errOpen := (dynamicLibraryLoader{}).Open(pluginFile{ID: "status-callback", Path: library}, host)
		if errOpen != nil {
			return nil, errOpen
		}
		defer client.Shutdown()
		return client.Call(context.Background(), method, request)
	})
}
