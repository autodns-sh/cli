//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A crashed pre-flock CLI leaves token.lock as a directory; the new flock
// implementation must recover it instead of failing with "is a directory".
func TestLockTokenFile_RecoversLegacyDirLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(os.Getenv("HOME"), ".config", "autodns", "token.lock")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	unlock, err := lockTokenFile()
	if err != nil {
		t.Fatalf("legacy dir lock not recovered: %v", err)
	}
	unlock()

	// After recovery, token.lock is a file and works again.
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		t.Fatalf("want lock file, got %v, err=%v", info, err)
	}
	unlock2, err := lockTokenFile()
	if err != nil {
		t.Fatalf("second lock failed: %v", err)
	}
	unlock2()
}

// A YOUNG empty legacy dir may belong to a running old CLI — do not break it
// instantly (would double-hold). It must age out instead.
func TestLockTokenFile_YoungLegacyDirWaits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(os.Getenv("HOME"), ".config", "autodns", "token.lock")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		unlock, err := lockTokenFile()
		if err == nil {
			unlock()
		}
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("young legacy dir lock was broken immediately")
	default:
	}
	// Simulate the old process finishing.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("lock after legacy holder released: %v", err)
	}
}
