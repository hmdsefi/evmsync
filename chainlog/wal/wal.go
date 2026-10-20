// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"github.com/hmdsefi/evmsync/chainlog"
)

// SyncPolicy selects when the WAL calls fsync.
type SyncPolicy uint8

// The sync policies.
const (
	SyncAlways SyncPolicy = iota // fsync before Append returns (default)
	SyncNever                    // never fsync; for tests only
)

// Options configures Open. The zero value is valid.
type Options struct {
	Meta        chainlog.Meta // used only when the directory is new
	SegmentSize int64         // roll threshold in bytes; 0 means 64 MiB
	Sync        SyncPolicy    // default SyncAlways
	WindowCap   int           // 0 means chainlog.DefaultWindowCap (100,000); at most MaxWindowCap
	Logger      *slog.Logger  // nil discards
}

// DefaultSegmentSize is the SegmentSize used when Options.SegmentSize is 0.
const DefaultSegmentSize = 64 << 20

// MaxWindowCap is the largest Options.WindowCap. The checkpoint holds the window, and a window of
// this many refs still fits the checkpoint's 1 GiB limit.
const MaxWindowCap = 10_000_000

const lockName = "lock"

// Log is a chainlog.Log backed by files in one directory: a lock file, the metadata, segment
// files of records and a checkpoint of the validator state. It is safe for concurrent use. Only
// one Log may have a directory open at a time, also across processes.
type Log struct {
	dir     string
	fsync   syncer // nil with SyncNever
	segSize int64
	logger  *slog.Logger
	hooks   hooks
	lock    *os.File
	meta    chainlog.Meta // normalized; never changes

	// appendMu serializes Append, Truncate and Close. Their writes to the fields below happen
	// with mu held as well, so a holder of appendMu may read them without mu.
	appendMu sync.Mutex
	buf      []byte   // Append's encode buffer; guarded by appendMu
	undelete []string // dropped segment files a failed deletion left, oldest first; guarded by appendMu
	starts   []int64  // the position of each record in buf; guarded by appendMu

	mu           sync.RWMutex
	segs         []*segment      // in offset order; the last is active; guarded by mu
	head         chainlog.Offset // guarded by mu
	oldest       chainlog.Offset // guarded by mu
	lastFin      chainlog.Offset // offset of the newest Finalize entry, 0 if none; guarded by mu
	state        chainlog.State  // guarded by mu
	checkpointAt chainlog.Offset // X of the checkpoint Open loaded, 0 without one
	failed       error           // the first write or fsync error; guarded by mu
	closed       bool            // guarded by mu
	wake         chan struct{}   // closed and replaced by every Append and by Close; guarded by mu
}

var _ chainlog.Log = (*Log)(nil)

// Open opens the WAL in dir, creating dir and a new log if dir holds none. It takes the
// directory's lock, recovers a torn tail and rebuilds the State.
//
// A new log gets opts.Meta, with a random ID when its ID is zero; an existing log keeps its own
// metadata and ignores opts.Meta. Open fails with an error wrapping ErrLocked when another Log
// holds dir, and ErrCorrupt when the files do not form a log the WAL wrote. On systems other than
// Unix it fails with an error wrapping errors.ErrUnsupported.
func Open(dir string, opts Options) (*Log, error) {
	return openWithHooks(dir, opts, hooks{})
}

func openWithHooks(dir string, opts Options, h hooks) (*Log, error) {
	switch {
	case opts.SegmentSize < 0:
		return nil, fmt.Errorf("wal: invalid options: segment size %d is negative", opts.SegmentSize)
	case opts.Sync > SyncNever:
		return nil, fmt.Errorf("wal: invalid options: unknown sync policy %d", opts.Sync)
	case opts.WindowCap < 0:
		return nil, fmt.Errorf("wal: invalid options: window cap %d is negative", opts.WindowCap)
	case opts.WindowCap > MaxWindowCap:
		return nil, fmt.Errorf("wal: invalid options: window cap %d is above %d", opts.WindowCap, MaxWindowCap)
	}
	l := &Log{
		dir:     dir,
		segSize: opts.SegmentSize,
		logger:  opts.Logger,
		hooks:   h,
		wake:    make(chan struct{}),
	}
	if l.segSize == 0 {
		l.segSize = DefaultSegmentSize
	}
	if opts.Sync == SyncAlways {
		l.fsync = func(f *os.File) error {
			if h.sync != nil {
				if err := h.sync(f.Name()); err != nil {
					return err
				}
			}
			return f.Sync()
		}
	}
	if l.logger == nil {
		l.logger = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(dir, dirMode); err != nil { //nolint:gosec // dir is the directory the caller chose for the log
		return nil, ioErr("open", dir, err)
	}
	lock, err := lockFile(filepath.Join(dir, lockName))
	if errors.Is(err, errWouldBlock) {
		return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
	}
	if err != nil {
		return nil, err
	}
	l.lock = lock
	created, err := l.load(opts)
	if err != nil {
		_ = l.closeFiles()
		return nil, err
	}
	l.logger.Info("wal opened",
		slog.String("dir", dir),
		slog.Int("segments", len(l.segs)),
		slog.Uint64("oldest", uint64(l.oldest)),
		slog.Uint64("head", uint64(l.head)),
		slog.Uint64("checkpoint", uint64(l.checkpointAt)),
		slog.Bool("new", created),
	)
	return l, nil
}

// check returns ErrClosed or ctx.Err(), in that order. The caller holds mu.
func (l *Log) check(ctx context.Context) error {
	if l.closed {
		return chainlog.ErrClosed
	}
	return ctx.Err()
}

// Meta returns the log's metadata.
func (l *Log) Meta(ctx context.Context) (chainlog.Meta, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := l.check(ctx); err != nil {
		return chainlog.Meta{}, err
	}
	m, _ := decodeMeta(encodeMeta(l.meta)) // a deep copy
	return m, nil
}

// Append validates entries in order against the log's State and appends all of them, or none
// with an error wrapping chainlog.ErrMalformed. It assigns offsets Head+1, Head+2, ... and
// returns the last one; with no entries it returns Head. With SyncAlways the entries are durable
// when it returns. After a write or fsync fails, it and every later Append and Truncate return
// an error wrapping ErrFailed.
func (l *Log) Append(ctx context.Context, entries ...chainlog.Entry) (chainlog.Offset, error) {
	l.appendMu.Lock()
	defer l.appendMu.Unlock()

	// CheckBatch changes the state while it runs, so it needs the write lock.
	l.mu.Lock()
	if err := l.check(ctx); err != nil {
		l.mu.Unlock()
		return 0, err
	}
	if l.failed != nil {
		l.mu.Unlock()
		return 0, fmt.Errorf("%w: earlier error: %w", ErrFailed, l.failed)
	}
	head := l.head
	if len(entries) == 0 {
		l.mu.Unlock()
		return head, nil
	}
	i, err := l.state.CheckBatch(entries)
	l.mu.Unlock()
	if err != nil {
		return 0, fmt.Errorf("chainlog: append entry %d of %d: %w", i+1, len(entries), err)
	}

	l.buf, l.starts = l.buf[:0], l.starts[:0]
	for i, e := range entries {
		e.Offset = head + 1 + chainlog.Offset(i) //nolint:gosec // i < len(entries)
		l.starts = append(l.starts, int64(len(l.buf)))
		l.buf = chainlog.Encode(l.buf, e)
	}
	if err := l.write(head, l.buf); err != nil {
		l.mu.Lock()
		l.failed = err
		l.mu.Unlock()
		return 0, fmt.Errorf("%w: append: %w", ErrFailed, err)
	}
	return l.publish(head, entries), nil
}

// publish makes the batch written after offset head visible: it applies the entries to the
// state, indexes them, moves Head and wakes every Wait. The caller holds appendMu.
func (l *Log) publish(head chainlog.Offset, entries []chainlog.Entry) chainlog.Offset {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.segs[len(l.segs)-1]
	for i, e := range entries {
		e.Offset = head + 1 + chainlog.Offset(i) //nolint:gosec // i < len(entries)
		l.state.Apply(e)
		if e.Kind == chainlog.KindFinalize {
			l.lastFin = e.Offset
		}
		if (e.Offset-s.first)%indexEvery == 0 {
			s.index = append(s.index, s.size+l.starts[i])
		}
	}
	s.size += int64(len(l.buf))
	s.last = head + chainlog.Offset(len(entries))
	l.head = s.last
	close(l.wake)
	l.wake = make(chan struct{})
	return l.head
}

// write writes the encoded batch b after offset head to the active segment, rolling to a new
// segment first when b does not fit, and fsyncs it with SyncAlways. The caller holds appendMu.
func (l *Log) write(head chainlog.Offset, b []byte) error {
	if s := l.segs[len(l.segs)-1]; s.size > 0 && s.size+int64(len(b)) > l.segSize {
		if err := l.roll(head); err != nil {
			return err
		}
	}
	s := l.segs[len(l.segs)-1]
	if _, err := l.hooks.writeTo(s.f, b); err != nil {
		return ioErr("write", s.f.Name(), err)
	}
	if l.fsync != nil {
		if err := l.fsync(s.f); err != nil {
			return ioErr("sync", s.f.Name(), err)
		}
	}
	return nil
}

// roll seals the active segment: it writes a checkpoint of the state after head, then creates
// the segment for head+1. The caller holds appendMu.
func (l *Log) roll(head chainlog.Offset) error {
	cp := encodeCheckpoint(checkpoint{x: head, l: l.lastFin, state: l.state})
	if err := writeFileAtomic(l.dir, checkpointName, cp, l.fsync); err != nil {
		return err
	}
	if err := l.createSegment(head + 1); err != nil {
		return err
	}
	l.logger.Debug("wal segment rolled", slog.String("segment", segmentName(head+1)), slog.Uint64("offset", uint64(head+1)))
	return nil
}

// Read returns up to max entries starting at offset from, in offset order. It returns nil when
// from is Head+1, an error wrapping chainlog.ErrTruncated when from is below Oldest, and an error
// wrapping chainlog.ErrAhead when from is above Head+1. A from of 0 reads from 1. It panics if
// max < 1. A record that fails its checksum is an error wrapping ErrCorrupt.
func (l *Log) Read(ctx context.Context, from chainlog.Offset, max int) ([]chainlog.Entry, error) {
	if from == 0 {
		from = 1
	}
	segs, to, err := l.span(ctx, from, max)
	if len(segs) == 0 {
		return nil, err
	}
	es := make([]chainlog.Entry, 0, to-from+1)
	for _, s := range segs {
		start := from
		if s.first > start {
			start = s.first
		}
		err := s.readRange(start, min(to, s.last), func(e chainlog.Entry) { es = append(es, e) })
		if err != nil {
			return nil, l.readError(from, err)
		}
	}
	return es, nil
}

// span checks a Read from offset from, at least 1, and returns copies of the segments that hold
// from..to, which Read then reads without the lock. It returns no segment when there is nothing to
// read.
func (l *Log) span(ctx context.Context, from chainlog.Offset, max int) ([]segment, chainlog.Offset, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := l.check(ctx); err != nil {
		return nil, 0, err
	}
	if max < 1 {
		panic("evmsync: chainlog: Read max must be at least 1, got " + strconv.Itoa(max))
	}
	switch {
	case from < l.oldest:
		return nil, 0, fmt.Errorf("%w: read from %d, oldest is %d", chainlog.ErrTruncated, from, l.oldest)
	case from == l.head+1:
		return nil, 0, nil
	case from > l.head+1:
		return nil, 0, fmt.Errorf("%w: read from %d, head is %d", chainlog.ErrAhead, from, l.head)
	}
	to := l.head
	if n := uint64(to - from); n >= uint64(max) {
		to = from + chainlog.Offset(max) - 1 //nolint:gosec // max <= n < head
	}
	var segs []segment
	for i := sort.Search(len(l.segs), func(i int) bool { return l.segs[i].last >= from }); i < len(l.segs) && l.segs[i].first <= to; i++ {
		segs = append(segs, *l.segs[i])
	}
	return segs, to, nil
}

// readError returns the error for a Read from offset from that failed with err. A file closed by
// Close or deleted by Truncate under the reader gives ErrClosed or chainlog.ErrTruncated.
func (l *Log) readError(from chainlog.Offset, err error) error {
	if errors.Is(err, ErrCorrupt) {
		return err
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return chainlog.ErrClosed
	}
	if from < l.oldest {
		return fmt.Errorf("%w: read from %d, oldest is %d", chainlog.ErrTruncated, from, l.oldest)
	}
	return err
}

// Wait returns nil once Head > after, ctx.Err() if ctx ends first, chainlog.ErrClosed if the log
// closes first, and an error wrapping chainlog.ErrAhead when after > Head.
func (l *Log) Wait(ctx context.Context, after chainlog.Offset) error {
	for {
		l.mu.RLock()
		if err := l.check(ctx); err != nil {
			l.mu.RUnlock()
			return err
		}
		head, wake := l.head, l.wake
		l.mu.RUnlock()
		switch {
		case head > after:
			return nil
		case after > head:
			return fmt.Errorf("%w: wait after %d, head is %d", chainlog.ErrAhead, after, head)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

// Head returns the offset of the newest entry, 0 if none was ever appended.
func (l *Log) Head(ctx context.Context) (chainlog.Offset, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := l.check(ctx); err != nil {
		return 0, err
	}
	return l.head, nil
}

// Oldest returns the offset of the first retained entry, Head+1 when none is retained.
func (l *Log) Oldest(ctx context.Context) (chainlog.Offset, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := l.check(ctx); err != nil {
		return 0, err
	}
	return l.oldest, nil
}

// State returns a copy of the validator state after the newest entry.
func (l *Log) State(ctx context.Context) (chainlog.State, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := l.check(ctx); err != nil {
		return chainlog.State{}, err
	}
	return l.state.Clone(), nil
}

// Truncate drops whole segments whose entries are all below min(before, L+1), where L is the
// offset of the newest Finalize entry (0 if none), and deletes their files. It keeps the active
// segment. A Read of a dropped range returns the entries or an error wrapping
// chainlog.ErrTruncated.
func (l *Log) Truncate(ctx context.Context, before chainlog.Offset) error {
	l.appendMu.Lock()
	defer l.appendMu.Unlock()
	l.mu.RLock()
	err := l.check(ctx)
	if err == nil && l.failed != nil {
		err = fmt.Errorf("%w: earlier error: %w", ErrFailed, l.failed)
	}
	b := min(before, l.lastFin+1)
	oldest := l.oldest
	l.mu.RUnlock()
	if err != nil {
		return err
	}
	// Files a failed deletion left go first: deleting a later segment before them would leave a
	// gap that Open rejects.
	if err := l.removeSegments(); err != nil {
		return err
	}
	if b <= oldest {
		return nil
	}
	return l.compact(b)
}

// Close closes every file, which releases the directory's lock, and wakes every Wait with
// chainlog.ErrClosed. It does not fsync: with SyncAlways everything is already durable. Later
// calls of other methods return chainlog.ErrClosed; later calls of Close return nil.
func (l *Log) Close() error {
	l.appendMu.Lock()
	defer l.appendMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	close(l.wake)
	return l.closeFiles()
}

// closeFiles closes the segment files and then the lock file.
func (l *Log) closeFiles() error {
	var errs []error
	for _, s := range l.segs {
		if err := s.f.Close(); err != nil {
			errs = append(errs, ioErr("close", s.f.Name(), err))
		}
	}
	if err := l.lock.Close(); err != nil {
		errs = append(errs, ioErr("close", l.lock.Name(), err))
	}
	return errors.Join(errs...)
}
