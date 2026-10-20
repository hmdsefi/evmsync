// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/hmdsefi/evmsync/chainlog"
)

const scanBuffer = 1 << 20

// segScan is what Open learns from reading one segment from its start.
type segScan struct {
	first, last chainlog.Offset // last == first-1 for an empty segment
	size        int64           // file size
	index       []int64         // file position of every 256th record
	end         int64           // end of the last good record
	bad         *badRecord      // first record that fails Decode or has the wrong offset; nil if none
}

// badRecord is the first record of a segment that fails.
type badRecord struct {
	pos, declaredEnd int64 // declaredEnd = pos + 8 + length, or -1 when fewer than 8 bytes remain
	err              error // the Decode error or "offset %d, want %d"
}

// scanSegment reads every record of f, a segment whose name gives first, and stops at the first
// bad record. It returns an error for I/O failures only.
func scanSegment(f *os.File, first chainlog.Offset) (segScan, error) {
	fi, err := f.Stat()
	if err != nil {
		return segScan{}, ioErr("read", f.Name(), err)
	}
	sc := segScan{first: first, last: first - 1, size: fi.Size()}
	r := bufio.NewReaderSize(io.NewSectionReader(f, 0, sc.size), scanBuffer)
	var buf []byte
	for sc.end < sc.size {
		rec, err := readRecord(r, &buf, sc.size-sc.end)
		if err != nil {
			return segScan{}, ioErr("read", f.Name(), err)
		}
		want := sc.last + 1
		n, err := decodeAt(rec, want, nil)
		if err != nil {
			declaredEnd := int64(-1)
			if len(rec) >= recHeader {
				declaredEnd = sc.end + recHeader + int64(binary.LittleEndian.Uint32(rec))
			}
			sc.bad = &badRecord{pos: sc.end, declaredEnd: declaredEnd, err: err}
			break
		}
		if (want-first)%indexEvery == 0 {
			sc.index = append(sc.index, sc.end)
		}
		sc.last = want
		sc.end += int64(n)
	}
	return sc, nil
}

// dirListing is what Open finds in the directory.
type dirListing struct {
	meta, checkpoint bool
	segments         []string // segment names in name order, which is offset order
}

// list removes the temporary files of interrupted writes and lists the names the WAL owns.
func (l *Log) list() (dirListing, error) {
	for _, tmp := range []string{metaName + ".tmp", checkpointName + ".tmp"} {
		path := filepath.Join(l.dir, tmp)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return dirListing{}, ioErr("remove", path, err)
		}
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return dirListing{}, ioErr("read", l.dir, err)
	}
	var d dirListing
	for _, e := range entries {
		switch name := e.Name(); name {
		case metaName:
			d.meta = true
		case checkpointName:
			d.checkpoint = true
		default:
			if _, ok := parseSegmentName(name); ok {
				d.segments = append(d.segments, name)
			}
		}
	}
	return d, nil
}

// load reads or creates the log in l.dir and leaves l ready for use. It reports whether the log
// was created. On error the caller closes l's files.
func (l *Log) load(opts Options) (created bool, err error) {
	d, err := l.list()
	if err != nil {
		return false, err
	}
	l.state = chainlog.NewState(opts.WindowCap)
	l.head, l.oldest = 0, 1

	if !d.meta && !d.checkpoint && len(d.segments) == 0 {
		return true, l.create(opts.Meta)
	}
	if !d.meta {
		return false, fmt.Errorf("%w: meta: missing", ErrCorrupt)
	}
	b, err := l.readFile(metaName, maxMetaSize)
	if err != nil {
		return false, err
	}
	if l.meta, err = decodeMeta(b); err != nil {
		return false, err
	}
	if len(d.segments) == 0 {
		if d.checkpoint {
			return false, fmt.Errorf("%w: no segment", ErrCorrupt)
		}
		// meta was written but the first segment was not: finish the creation.
		return true, l.createSegment(1)
	}

	cp := checkpoint{state: chainlog.NewState(opts.WindowCap)}
	if d.checkpoint {
		b, err := l.readFile(checkpointName, maxCheckpointSize)
		if err != nil {
			return false, err
		}
		if cp, err = decodeCheckpoint(b, opts.WindowCap); err != nil {
			return false, err
		}
		l.checkpointAt = cp.x
	}
	cut, err := l.openSegments(d.segments)
	if err != nil {
		return false, err
	}

	f0, h := l.segs[0].first, l.segs[len(l.segs)-1].last
	if d.checkpoint {
		if cp.x < f0-1 || cp.x > h {
			return false, fmt.Errorf("%w: checkpoint at %d outside [%d, %d]", ErrCorrupt, cp.x, f0-1, h)
		}
	} else if f0 != 1 {
		return false, fmt.Errorf("%w: first segment starts at %d without a checkpoint", ErrCorrupt, f0)
	}
	if cut != nil {
		if err := cut(); err != nil {
			return false, err
		}
	}
	l.state, l.lastFin = cp.state, cp.l
	if err := l.replay(cp.x+1, h); err != nil {
		return false, err
	}
	l.head, l.oldest = h, f0
	return false, nil
}

// create writes a new log: meta, then the first segment.
func (l *Log) create(m chainlog.Meta) error {
	if err := m.Filter.Validate(); err != nil {
		return fmt.Errorf("wal: invalid options: meta: %w", err)
	}
	if m.ID.IsZero() {
		_, _ = rand.Read(m.ID[:]) // never fails since Go 1.24
	}
	b := encodeMeta(m)
	l.meta, _ = decodeMeta(b) // a normalized deep copy; b is valid
	if err := writeFileAtomic(l.dir, metaName, b, l.fsync); err != nil {
		return err
	}
	return l.createSegment(1)
}

// createSegment creates the empty segment for offset first and makes it the active one.
func (l *Log) createSegment(first chainlog.Offset) error {
	name := segmentName(first)
	path := filepath.Join(l.dir, name)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_APPEND, fileMode) //nolint:gosec // a segment name the WAL makes
	if err != nil {
		return ioErr("open", path, err)
	}
	if err := syncDir(l.dir, l.fsync); err != nil {
		_ = f.Close()
		return err
	}
	l.mu.Lock()
	l.segs = append(l.segs, &segment{name: name, first: first, last: first - 1, f: f})
	l.mu.Unlock()
	return nil
}

// openSegments opens and scans the segments, in order, and checks that they fit together. A torn
// tail of the last segment is not cut yet: it returns the cut, which load runs only once the
// checkpoint agrees with the segments, so a failed Open changes no file.
func (l *Log) openSegments(names []string) (cut func() error, err error) {
	for i, name := range names {
		first, _ := parseSegmentName(name)
		if i > 0 {
			if prev := l.segs[i-1].last; first != prev+1 {
				return nil, fmt.Errorf("%w: segment %s does not follow offset %d", ErrCorrupt, name, prev)
			}
		} else if first == 0 {
			return nil, fmt.Errorf("%w: segment %s does not follow offset 0", ErrCorrupt, name)
		}
		active := i == len(names)-1
		flag := os.O_RDONLY
		if active {
			flag = os.O_RDWR | os.O_APPEND
		}
		path := filepath.Join(l.dir, name)
		f, err := os.OpenFile(path, flag, 0) //nolint:gosec // a segment name the WAL owns
		if err != nil {
			return nil, ioErr("open", path, err)
		}
		s := &segment{name: name, first: first, f: f}
		l.segs = append(l.segs, s)
		sc, err := scanSegment(f, first)
		if err != nil {
			return nil, err
		}
		if sc.bad != nil {
			torn := false
			if active {
				if torn, err = tornTail(f, sc); err != nil {
					return nil, err
				}
			}
			if !torn {
				return nil, fmt.Errorf("%w: segment %s position %d: %w", ErrCorrupt, name, sc.bad.pos, sc.bad.err)
			}
			cut = func() error { return l.cutTail(f, name, sc) }
		}
		if !active && sc.last < sc.first {
			return nil, fmt.Errorf("%w: segment %s is empty", ErrCorrupt, name)
		}
		s.last, s.size, s.index = sc.last, sc.end, sc.index
	}
	return cut, nil
}

// replay applies the entries from..to to l.state, as they follow the checkpoint, and records
// the offset of each Finalize.
func (l *Log) replay(from, to chainlog.Offset) error {
	for _, s := range l.segs {
		if from > to {
			return nil
		}
		if s.last < from {
			continue
		}
		end := min(to, s.last)
		err := s.readRange(from, end, func(e chainlog.Entry) {
			l.state.Apply(e)
			if e.Kind == chainlog.KindFinalize {
				l.lastFin = e.Offset
			}
			if l.hooks.replay != nil {
				l.hooks.replay(e.Offset)
			}
		})
		if err != nil {
			return err
		}
		from = end + 1
	}
	return nil
}

// readFile reads the meta or checkpoint file name, at most the 8-byte header, a body of maxSize
// bytes and one byte more: enough for the decoder to report a file that is too long.
func (l *Log) readFile(name string, maxSize uint32) ([]byte, error) {
	path := filepath.Join(l.dir, name)
	f, err := os.Open(path) //nolint:gosec // a file name the WAL owns
	if err != nil {
		return nil, ioErr("open", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, ioErr("read", path, err)
	}
	b := make([]byte, min(fi.Size(), fileHeader+int64(maxSize)+1))
	n, err := io.ReadFull(f, b)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, ioErr("read", path, err)
	}
	return b[:n], nil
}

// tornTail reports whether the bad record that ends sc's scan of the last segment f is the tail
// of an interrupted write rather than corruption: fewer than 8 bytes remain, the record's
// declared end is at or past the end of the file, or every byte from it on is zero. A bad record
// whose declared end lies inside the file and is followed by more data is corruption, so the WAL
// does not drop the records after it. (A damaged length field that points past the end of the
// file looks like a torn write; the checkpoint check that runs before the cut catches the case
// where that would drop records the checkpoint covers.)
func tornTail(f *os.File, sc segScan) (bool, error) {
	p := sc.bad.pos
	if sc.bad.declaredEnd < 0 || sc.bad.declaredEnd >= sc.size {
		return true, nil
	}
	buf := make([]byte, readBuffer)
	for p < sc.size {
		n, err := f.ReadAt(buf[:min(int64(len(buf)), sc.size-p)], p)
		if err != nil {
			return false, ioErr("read", f.Name(), err)
		}
		for _, b := range buf[:n] {
			if b != 0 {
				return false, nil
			}
		}
		p += int64(n)
	}
	return true, nil
}

// cutTail truncates the last segment f to the end of its last whole record.
func (l *Log) cutTail(f *os.File, name string, sc segScan) error {
	if err := f.Truncate(sc.bad.pos); err != nil {
		return ioErr("truncate", f.Name(), err)
	}
	// Logged before the fsync: the bytes are gone even if the fsync fails.
	l.logger.Warn("wal torn tail cut",
		slog.String("segment", name),
		slog.Int64("position", sc.bad.pos),
		slog.Int64("bytes", sc.size-sc.bad.pos),
	)
	if l.fsync != nil {
		if err := l.fsync(f); err != nil {
			return ioErr("sync", f.Name(), err)
		}
	}
	return nil
}
