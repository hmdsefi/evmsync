// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import "errors"

var (
	// ErrCorrupt is returned by Open and Read when a file of the log holds bytes the WAL did not
	// write: a bad record before the tail, a bad meta or checkpoint file, or segments that do not
	// fit together. The WAL never repairs it; a corrupt log needs a new directory.
	ErrCorrupt = errors.New("wal: corrupt log")
	// ErrLocked is returned by Open when another open log holds the directory's lock.
	ErrLocked = errors.New("wal: directory locked")
	// ErrFailed is returned by Append and Truncate after an earlier write or fsync failed. The
	// log stays readable up to Head; reopening it recovers the files.
	ErrFailed = errors.New("wal: log failed")
)
