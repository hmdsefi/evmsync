// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"fmt"
	"log/slog"
	"os"
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
	if err := writeFileAtomic(l.dir, checkpointName, cp, l.sync); err != nil {
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
	deleted := 0
	for _, s := range dropped {
		path := filepath.Join(l.dir, s.name)
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("wal: remove %s: %w", path, err)
		}
		deleted++
		if l.hooks.removed != nil && !l.hooks.removed(s.name) {
			return nil
		}
	}
	if l.sync {
		if err := syncDir(l.dir); err != nil {
			return err
		}
	}
	l.logger.Info("wal compacted",
		slog.Uint64("before", uint64(b)),
		slog.Int("segments_deleted", deleted),
		slog.Uint64("oldest", uint64(oldest)),
	)
	return nil
}
