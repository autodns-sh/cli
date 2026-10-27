//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lockTokenFile on Windows: atomic lock-directory creation (flock is not
// available). A stale lock is broken only when its ownership token is
// unchanged across two reads 150ms apart — never remove a lock whose
// contents changed, it may belong to a live process.
func lockTokenFile() (func(), error) {
	path := filepath.Join(os.Getenv("HOME"), ".config", "autodns", "token.lock")
	tokenFile := filepath.Join(path, "owner")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	owner := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := os.Mkdir(path, 0700); err == nil {
			if err := os.WriteFile(tokenFile, []byte(owner), 0600); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return func() {
				// Remove only our own token before releasing the dir.
				if data, err := os.ReadFile(tokenFile); err == nil && string(data) == owner {
					_ = os.Remove(tokenFile)
					_ = os.Remove(path)
				}
			}, nil
		} else if !os.IsExist(err) {
			return nil, fmt.Errorf("locking token file: %w", err)
		}
		// Stale check: same owner token twice, older than 2 minutes.
		first, err1 := os.ReadFile(tokenFile)
		info, err2 := os.Stat(path)
		if err1 == nil && err2 == nil && time.Since(info.ModTime()) > 2*time.Minute {
			time.Sleep(150 * time.Millisecond)
			second, err3 := os.ReadFile(tokenFile)
			if err3 == nil && string(first) == string(second) {
				_ = os.Remove(tokenFile)
				if err := os.Remove(path); err == nil {
					continue
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for token lock")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
