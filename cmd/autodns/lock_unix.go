//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockTokenFile takes an exclusive flock on a persistent lock file. The file
// is never deleted, so there is no stale-lock recovery path to race: the
// kernel releases the lock if the process dies.
//
// Migration: pre-flock CLI versions used token.lock as a lock DIRECTORY.
// A crashed old process leaves that directory behind; recover it before
// opening the file. Only an empty directory is removed — a directory whose
// owner file still exists may belong to a live old-version process.
func lockTokenFile() (func(), error) {
	path := filepath.Join(os.Getenv("HOME"), ".config", "autodns", "token.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		// Legacy dir lock. Old versions wrote no content, so any empty dir
		// older than 2 minutes is a stale leftover; a fresh one may be held
		// by a running old CLI — wait for it via the retry loop below.
		if entries, _ := os.ReadDir(path); len(entries) == 0 && time.Since(info.ModTime()) > 2*time.Minute {
			_ = os.Remove(path)
		} else if len(entries) == 0 {
			// Young empty legacy dir: could be a live old process. flock on a
			// sibling file cannot synchronize with it; wait for it to age out.
			deadline := time.Now().Add(30 * time.Second)
			for {
				if _, err := os.Stat(path); os.IsNotExist(err) {
					break
				}
				if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > 2*time.Minute {
					_ = os.Remove(path)
					break
				}
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("timed out waiting for legacy token lock")
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
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
