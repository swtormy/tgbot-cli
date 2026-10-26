package mtp

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/session"
)

// fileStorage persists the session atomically (write, fsync, rename). A torn
// write of the auth key would force a new login, so the last good copy is also
// kept as <path>.bak and used if the main file is unreadable.
type fileStorage struct {
	path string
	mu   sync.Mutex
}

func (f *fileStorage) LoadSession(_ context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range []string{f.path, f.path + ".bak"} {
		data, err := os.ReadFile(p)
		if err == nil && len(data) > 0 {
			return data, nil
		}
	}
	return nil, session.ErrNotFound
}

func (f *fileStorage) StoreSession(_ context.Context, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, err := os.ReadFile(f.path); err == nil && len(old) > 0 {
		_ = writeAtomic(f.path+".bak", old)
	}
	return writeAtomic(f.path, data)
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil && !isWindows() {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
