// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"errors"
	"log/slog"
	"path/filepath"

	"github.com/hmdsefi/evmsync/chainlog"
)

// compact deletes the segments, other than the active one, whose entries are all below b. It
// first writes a checkpoint at Head, so the deleted entries are never needed again, then drops
// the segments from the index and deletes their files oldest first. A crash at any point leaves a
// log that Open accepts. The caller holds appendMu.
func (l *Log) compact(b chainlog.Offset) error {
	n := 0
	for n < len(l.segs)-1 && l.segs[n].last < b {
		n++
	}
	if n == 0 {
		return nil
	}
	cp := encodeCheckpoint(checkpoint{x: l.head, l: l.lastFin, state: l.state})
	if err := writeFileAtomic(l.dir, checkpointName, cp, l.fsync); err != nil {
		return err
	}

	l.mu.Lock()
	dropped := l.segs[:n:n]
	l.segs = l.segs[n:]
	l.oldest = l.segs[0].first
	oldest := l.oldest
	l.mu.Unlock()

	// A reader that copied a dropped segment fails its ReadAt once the file is closed, and then
	// reports chainlog.ErrTruncated.
	for _, s := range dropped {
		_ = s.f.Close() // a read-only file; its close error tells nothing
	}
	for _, s := range dropped {
		l.undelete = append(l.undelete, s.name)
	}
	if err := l.removeSegments(); err != nil {
		if errors.Is(err, errStopped) {
			return nil
		}
		return err
	}
	if err := syncDir(l.dir, l.fsync); err != nil {
		return err
	}
	l.logger.Info("wal compacted",
		slog.Uint64("before", uint64(b)),
		slog.Int("segments_deleted", len(dropped)),
		slog.Uint64("oldest", uint64(oldest)),
	)
	return nil
}

// errStopped is how removeSegments reports that hooks.removed stopped it, as a crash would.
var errStopped = errors.New("wal: compaction stopped")

// removeSegments deletes the dropped segment files in l.undelete, oldest first. On a failure it
// stops and keeps the rest, so the next Truncate retries them before it deletes any other segment.
// The caller holds appendMu.
func (l *Log) removeSegments() error {
	for len(l.undelete) > 0 {
		name := l.undelete[0]
		path := filepath.Join(l.dir, name)
		if err := l.hooks.removeFile(path); err != nil {
			return ioErr("remove", path, err)
		}
		l.undelete = l.undelete[1:]
		if l.hooks.removed != nil && !l.hooks.removed(name) {
			l.undelete = nil // a simulated crash: the process stops here
			return errStopped
		}
	}
	return nil
}
