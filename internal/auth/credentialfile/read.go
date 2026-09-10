package credentialfile

import "os"

// ReadFile reads a complete credential snapshot while excluding in-process
// writers. The lock is released before callers parse data or invoke hooks.
// Callers that already hold Lock for this path must use os.ReadFile directly.
func ReadFile(path string) ([]byte, error) {
	unlock := Lock(path)
	defer unlock()
	return os.ReadFile(path)
}
