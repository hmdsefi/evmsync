// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package wal

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// errWouldBlock reports that another open file description holds the lock.
var errWouldBlock = errors.New("lock held")

// lockFile opens path, creating it, and takes a non-blocking exclusive flock on it. The kernel
// releases the lock when the file is closed or the process dies.
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, fileMode) //nolint:gosec // path is the log's own lock file
	if err != nil {
		return nil, fmt.Errorf("wal: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // a file descriptor fits an int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errWouldBlock
		}
		return nil, fmt.Errorf("wal: lock %s: %w", path, err)
	}
	return f, nil
}
