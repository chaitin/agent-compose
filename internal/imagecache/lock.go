package imagecache

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type UnlockFunc func() error

func (c *Cache) Lock() (UnlockFunc, error) {
	if err := c.Ensure(); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(c.config.Root, ".lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, NewError(ErrorKindInternal, "lock", lockPath, err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		_ = lockFile.Close()
		return nil, NewError(ErrorKindInternal, "lock", lockPath, err)
	}
	return func() error {
		unlockErr := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		closeErr := lockFile.Close()
		if unlockErr != nil {
			return fmt.Errorf("unlock image cache: %w", unlockErr)
		}
		return closeErr
	}, nil
}

func (c *Cache) WithLock(fn func() error) error {
	unlock, err := c.Lock()
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	return fn()
}

func (c *Cache) TempDir(name string) (string, error) {
	if err := c.Ensure(); err != nil {
		return "", err
	}
	tmpRoot := filepath.Join(c.config.Root, "tmp")
	if err := ensureDir(tmpRoot); err != nil {
		return "", NewError(ErrorKindInternal, "tempdir", tmpRoot, err)
	}
	dir, err := os.MkdirTemp(tmpRoot, sanitizePathSegment(name)+"-*")
	if err != nil {
		return "", NewError(ErrorKindInternal, "tempdir", tmpRoot, err)
	}
	return dir, nil
}

func WriteReadyFlag(path string) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return NewError(ErrorKindInternal, "ready", path, err)
	}
	return os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

func ReadyFlagExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
