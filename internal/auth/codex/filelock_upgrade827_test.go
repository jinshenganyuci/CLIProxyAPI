package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestUpgrade827CredentialLockProcess(t *testing.T) {
	path := os.Getenv("CPA_TEST_CREDENTIAL_LOCK_PATH")
	if path == "" {
		return
	}
	for range 40 {
		if err := WithFileLock(path, func() error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var data map[string]any
			if err := json.Unmarshal(raw, &data); err != nil {
				return err
			}
			data["count"] = data["count"].(float64) + 1
			runtime.Gosched()
			return WriteJSONFileAtomic(path, data)
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpgrade827CredentialLockSerializesIndependentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.json")
	if err := WriteJSONFileAtomic(path, map[string]any{"count": 0, "proxy_url": "direct", "concurrency_limit": 3}); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var commands []*exec.Cmd
	var outputs []*bytes.Buffer
	for range 3 {
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestUpgrade827CredentialLockProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), "CPA_TEST_CREDENTIAL_LOCK_PATH="+path)
		output := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		commands = append(commands, cmd)
		outputs = append(outputs, output)
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s", i, err, outputs[i])
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data["count"] != float64(120) || data["proxy_url"] != "direct" || data["concurrency_limit"] != float64(3) {
		t.Fatalf("cross-process update lost data: %s", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o", info.Mode().Perm())
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".codex-token-*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary credential files remain: %v, %v", leftovers, err)
	}
}
