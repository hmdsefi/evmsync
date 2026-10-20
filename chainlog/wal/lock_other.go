// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package wal

import (
	"errors"
	"fmt"
	"os"
)

// errWouldBlock reports that another open file description holds the lock.
var errWouldBlock = errors.New("lock held")

// lockFile fails: the WAL needs flock, which only Unix systems have.
func lockFile(path string) (*os.File, error) {
	return nil, fmt.Errorf("wal: lock %s: %w", path, errors.ErrUnsupported)
}
