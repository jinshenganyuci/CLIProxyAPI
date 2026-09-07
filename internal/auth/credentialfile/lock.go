// Package credentialfile coordinates in-process updates to credential files.
package credentialfile

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type pathLock struct {
	mu   sync.Mutex
	refs int
}

var locks = struct {
	sync.Mutex
	paths map[string]*pathLock
}{paths: make(map[string]*pathLock)}

// Lock serializes read-modify-write operations for a credential path. Callers
// must release it before calling an auth manager or invoking external hooks.
func Lock(path string) func() {
	key := filepath.Clean(path)
	if absolute, errAbs := filepath.Abs(key); errAbs == nil {
		key = absolute
	}
	if directory, errResolve := filepath.EvalSymlinks(filepath.Dir(key)); errResolve == nil {
		key = filepath.Join(directory, filepath.Base(key))
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	locks.Lock()
	entry := locks.paths[key]
	if entry == nil {
		entry = &pathLock{}
		locks.paths[key] = entry
	}
	entry.refs++
	locks.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		locks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(locks.paths, key)
		}
		locks.Unlock()
	}
}
