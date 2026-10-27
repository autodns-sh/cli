//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockTokenFile takes an exclusive flock on a persistent lock file. The file
// is never deleted, so there is no stale-lock recovery path to race: the
// kernel releases the lock if the process dies.
func lockTokenFile() (func(), error) {
	path := filepath.Join(os.Getenv("HOME"), ".config", "autodns", "token.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking token file: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
