package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type operationLock struct {
	file  *os.File
	path  string
	owner string
	once  sync.Once
}

func acquireOperationLock(name string) (*operationLock, error) {
	return acquireOperationLockWithWait(name, 0)
}

func acquireOperationLockWithWait(name string, wait time.Duration) (*operationLock, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".claude-swap")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".lock")
	deadline := time.Now().Add(wait)
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			owner := "another process"
			if data, readErr := os.ReadFile(path); readErr == nil {
				owner = strings.TrimSpace(string(data))
				if pid, parseErr := strconv.Atoi(owner); parseErr == nil && !processAlive(pid) {
					if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
						return nil, fmt.Errorf("remove stale operation lock: %w", err)
					}
					continue
				}
			}
			if time.Now().Before(deadline) {
				time.Sleep(min(50*time.Millisecond, time.Until(deadline)))
				continue
			}
			return nil, fmt.Errorf("another Claude Desktop Swap operation is active (%s)", owner)
		}
		if err != nil {
			return nil, err
		}
		owner := strconv.Itoa(os.Getpid())
		if _, err := file.WriteString(owner); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, err
		}
		return &operationLock{file: file, path: path, owner: owner}, nil
	}
}

func operationLockHeld(name string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude-swap", name+".lock"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return err != nil || processAlive(pid)
}

func (l *operationLock) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		_ = l.file.Close()
		data, err := os.ReadFile(l.path)
		if err != nil || strings.TrimSpace(string(data)) != l.owner {
			return
		}
		_ = os.Remove(l.path)
	})
}
