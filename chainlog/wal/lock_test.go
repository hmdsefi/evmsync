// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package wal

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestLock(t *testing.T) {
	dir := t.TempDir()
	l1, err := Open(dir, testOpts(0))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = Open(dir, testOpts(0))
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open = %v, want one wrapping ErrLocked", err)
	}
	if got, want := err.Error(), "wal: directory locked: "+dir; got != want {
		t.Errorf("second Open error = %q, want %q", got, want)
	}
	mustClose(t, l1)
	l2, err := Open(dir, testOpts(0))
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	mustClose(t, l2)

	// A process killed while it holds the lock leaves the directory openable.
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelper$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), "EVMSYNC_WAL_LOCK_CHILD="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	lines := bufio.NewScanner(stdout)
	locked := false
	for !locked && lines.Scan() {
		locked = lines.Text() == "locked"
	}
	if !locked {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("the child exited without taking the lock")
	}
	if err := cmd.Process.Kill(); err != nil { // SIGKILL
		t.Fatalf("Kill: %v", err)
	}
	_ = cmd.Wait()
	l3, err := Open(dir, testOpts(0))
	if err != nil {
		t.Fatalf("Open after the child was killed: %v", err)
	}
	mustClose(t, l3)
}

// TestLockHelper is the child process of TestLock: it opens the directory, prints "locked" and
// blocks on stdin until it is killed.
func TestLockHelper(t *testing.T) {
	dir := os.Getenv("EVMSYNC_WAL_LOCK_CHILD")
	if dir == "" {
		t.Skip("runs only as TestLock's child process")
	}
	l, err := Open(dir, testOpts(0))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	_, _ = os.Stdout.WriteString("locked\n")
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
}
