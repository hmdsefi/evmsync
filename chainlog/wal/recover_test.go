// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/hmdsefi/evmsync/chainlog"
)

// recoveryOpts are testOpts with the default window cap, so a test can append many blocks without
// finalizing.
func recoveryOpts(segmentSize int64) Options {
	o := testOpts(segmentSize)
	o.WindowCap = 0
	return o
}

// recordEnds returns the end of each record of es, numbered from first, in a segment that starts
// with first.
func recordEnds(es []chainlog.Entry, first chainlog.Offset) []int64 {
	var ends []int64
	var pos int64
	for i, e := range es {
		e.Offset = first + chainlog.Offset(i) //nolint:gosec // i < len(es)
		pos += int64(len(chainlog.Encode(nil, e)))
		ends = append(ends, pos)
	}
	return ends
}

// encoded returns the records of es numbered from 1, concatenated.
func encoded(es []chainlog.Entry) []byte {
	var b []byte
	for i, e := range es {
		e.Offset = chainlog.Offset(i + 1) //nolint:gosec // i < len(es)
		b = chainlog.Encode(b, e)
	}
	return b
}

// asRead returns the records of es with their own offsets, concatenated: what Read returned.
func asRead(es []chainlog.Entry) []byte {
	var b []byte
	for _, e := range es {
		b = chainlog.Encode(b, e)
	}
	return b
}

// thirtyBlocks writes 30 linked blocks with one log each, one per Append, in 1 KiB segments and
// returns the directory and the entries.
func thirtyBlocks(t *testing.T) (string, []chainlog.Entry) {
	t.Helper()
	dir := t.TempDir()
	es := linkedBlocks(1, 30, 40)
	l, err := Open(dir, recoveryOpts(1024))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, e := range es {
		mustAppend(t, l, e)
	}
	mustClose(t, l)
	return dir, es
}

// reopen opens dir with a recording logger and returns the log, its records and the open error.
func reopen(t *testing.T, dir string) (*Log, *recordHandler, error) {
	t.Helper()
	logger, rec := newRecorder()
	opts := recoveryOpts(1024)
	opts.Logger = logger
	l, err := Open(dir, opts)
	return l, rec, err
}

// wantEntries checks that l holds exactly the first n of es, byte for byte.
func wantEntries(t *testing.T, l *Log, es []chainlog.Entry, n int) {
	t.Helper()
	if h, _ := l.Head(t.Context()); h != chainlog.Offset(n) { //nolint:gosec // n <= 30
		t.Fatalf("Head = %d, want %d", h, n)
	}
	got, err := l.Read(t.Context(), 1, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(asRead(got), encoded(es[:n])) {
		t.Errorf("entries 1..%d differ from the originals", n)
	}
}

func TestRecoverEveryCut(t *testing.T) {
	base, es := thirtyBlocks(t)
	names := segmentNames(t, base)
	last := names[len(names)-1]
	lastFirst, _ := parseSegmentName(last)
	ends := recordEnds(es[lastFirst-1:], lastFirst)
	size := listing(t, base)[last]
	if size != ends[len(ends)-1] {
		t.Fatalf("test setup: last segment holds %d bytes, want %d", size, ends[len(ends)-1])
	}

	for c := int64(0); c <= size; c++ {
		dir := copyDir(t, base)
		path := filepath.Join(dir, last)
		if err := os.Truncate(path, c); err != nil {
			t.Fatal(err)
		}
		l, rec, err := reopen(t, dir)
		if err != nil {
			t.Fatalf("cut at %d: Open: %v", c, err)
		}
		kept, keep := 0, int64(0)
		for kept < len(ends) && ends[kept] <= c {
			keep = ends[kept]
			kept++
		}
		wantEntries(t, l, es, index(lastFirst)-1+kept)

		attrs, warned := rec.find(slog.LevelWarn, "wal torn tail cut")
		if c == keep {
			if warned {
				t.Errorf("cut at %d, a record boundary: warning %v, want none", c, attrs)
			}
		} else {
			wantSame(t, "cut at "+strconv.FormatInt(c, 10)+": warning", attrs,
				map[string]any{"segment": last, "position": keep, "bytes": c - keep})
			if got := listing(t, dir)[last]; got != keep {
				t.Errorf("cut at %d: segment holds %d bytes after Open, want %d", c, got, keep)
			}
		}
		head, _ := l.Head(t.Context())
		if off := mustAppend(t, l, linkedBlocks(uint64(head)+1, 1, 40)...); off != head+1 {
			t.Errorf("cut at %d: Append returned %d, want %d", c, off, head+1)
		}
		mustClose(t, l)
	}
}

func TestTornVersusCorrupt(t *testing.T) {
	base, es := thirtyBlocks(t)
	names := segmentNames(t, base)
	last := names[len(names)-1]
	lastFirst, _ := parseSegmentName(last)
	ends := recordEnds(es[lastFirst-1:], lastFirst)
	if len(ends) < 2 {
		t.Fatalf("test setup: last segment holds %d records, want at least 2", len(ends))
	}
	lastStart := ends[len(ends)-2] // start of the last record
	prevStart := int64(0)          // start of the second-to-last record
	if len(ends) > 2 {
		prevStart = ends[len(ends)-3]
	}
	wrongOffset := es[29]
	wrongOffset.Offset = 99
	reencoded := chainlog.Encode(nil, wrongOffset)
	f1, _ := parseSegmentName(names[1])
	sealedEnds := recordEnds(es[:f1-1], 1)
	sealedLast := int64(0) // start of the last record of the first segment
	if len(sealedEnds) > 1 {
		sealedLast = sealedEnds[len(sealedEnds)-2]
	}

	tests := []struct {
		name    string
		damage  func(dir string)
		head    int    // the Head Open recovers to, for a torn tail
		corrupt string // the error prefix after "wal: corrupt log: ", for corruption
	}{
		{"zeros after the last record", func(dir string) {
			rewrite(t, dir, last, func(b []byte) []byte { return append(b, make([]byte, 4096)...) })
		}, 30, ""},
		{"flipped body byte in the last record", func(dir string) {
			rewrite(t, dir, last, func(b []byte) []byte { b[lastStart+20] ^= 1; return b })
		}, 29, ""},
		{"wrong offset in the last record", func(dir string) {
			rewrite(t, dir, last, func(b []byte) []byte { return append(b[:lastStart], reencoded...) })
		}, 29, ""},
		{"flipped body byte in the second-to-last record", func(dir string) {
			rewrite(t, dir, last, func(b []byte) []byte { b[prevStart+20] ^= 1; return b })
		}, 0, "segment " + last + " position " + strconv.FormatInt(prevStart, 10) + ": "},
		{"a non-zero first byte in trailing bytes", func(dir string) {
			rewrite(t, dir, last, func(b []byte) []byte {
				tail := make([]byte, 4096)
				tail[0] = 1
				return append(b, tail...)
			})
		}, 0, "segment " + last + " position " + strconv.FormatInt(ends[len(ends)-1], 10) + ": "},
		{"flipped body byte in the last record of a sealed segment", func(dir string) {
			rewrite(t, dir, names[0], func(b []byte) []byte { b[sealedLast+20] ^= 1; return b })
		}, 0, "segment " + names[0] + " position " + strconv.FormatInt(sealedLast, 10) + ": "},
		{"zeros, then one byte past the first 64 KiB", func(dir string) {
			rewrite(t, dir, last, func(b []byte) []byte {
				tail := make([]byte, 70<<10)
				tail[66_000] = 1
				return append(b, tail...)
			})
		}, 0, "segment " + last + " position " + strconv.FormatInt(ends[len(ends)-1], 10) + ": "},
		{"a zero header, then a record", func(dir string) {
			rewrite(t, dir, last, func(b []byte) []byte { return append(append(b, make([]byte, 8)...), reencoded...) })
		}, 0, "segment " + last + " position " + strconv.FormatInt(ends[len(ends)-1], 10) + ": "},
		{"a damaged last record, then one byte", func(dir string) {
			rewrite(t, dir, last, func(b []byte) []byte { b[lastStart+20] ^= 1; return append(b, 0x01) })
		}, 0, "segment " + last + " position " + strconv.FormatInt(lastStart, 10) + ": "},
		{"a cut below the checkpoint", func(dir string) {
			// The checkpoint covers every record; a length flip in the active segment's first
			// record would cut them. Open must refuse before it cuts.
			b, _ := os.ReadFile(filepath.Join(dir, checkpointName))
			cp, _ := decodeCheckpoint(b, 0)
			cp.x = 30
			rewrite(t, dir, checkpointName, func([]byte) []byte { return encodeCheckpoint(cp) })
			rewrite(t, dir, last, func(b []byte) []byte { b[3] ^= 0x80; return b })
		}, 0, "checkpoint at 30 outside ["},
		{"flipped byte in the first segment", func(dir string) {
			rewrite(t, dir, names[0], func(b []byte) []byte { b[20] ^= 1; return b })
		}, 0, "segment " + names[0] + " position 0: "},
		{"second segment renamed one higher", func(dir string) {
			_ = os.Rename(filepath.Join(dir, names[1]), filepath.Join(dir, segmentName(f1+1)))
		}, 0, "segment " + segmentName(f1+1) + " does not follow offset"},
		{"empty sealed segment", func(dir string) {
			_ = os.Truncate(filepath.Join(dir, names[1]), 0)
		}, 0, "segment " + names[1] + " is empty"},
		{"no segment", func(dir string) {
			for _, n := range names {
				_ = os.Remove(filepath.Join(dir, n))
			}
		}, 0, "no segment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := copyDir(t, base)
			tt.damage(dir)
			before := listing(t, dir)
			l, rec, err := reopen(t, dir)
			if tt.corrupt != "" {
				if err == nil {
					_ = l.Close()
					t.Fatal("Open succeeded, want ErrCorrupt")
				}
				if !errors.Is(err, ErrCorrupt) || !strings.HasPrefix(err.Error(), "wal: corrupt log: "+tt.corrupt) {
					t.Errorf("Open = %v, want ErrCorrupt with the prefix %q", err, tt.corrupt)
				}
				wantSame(t, "files after a failed Open", listing(t, dir), before)
				return
			}
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer l.Close()
			wantEntries(t, l, es, tt.head)
			if _, warned := rec.find(slog.LevelWarn, "wal torn tail cut"); !warned {
				t.Error("no torn tail warning")
			}
			if got, want := listing(t, dir)[last], ends[tt.head-index(lastFirst)]; got != want {
				t.Errorf("last segment holds %d bytes after Open, want %d", got, want)
			}
		})
	}
}

// TestRecoverSync cuts a torn tail with SyncAlways: Open fsyncs the cut segment after the cut,
// and a failed fsync fails Open with the I/O error.
func TestRecoverSync(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run("fsync fails "+strconv.FormatBool(fail), func(t *testing.T) {
			dir, _ := thirtyBlocks(t)
			names := segmentNames(t, dir)
			last := names[len(names)-1]
			path := filepath.Join(dir, last)
			fi, _ := os.Stat(path)
			cutAt := fi.Size()
			rewrite(t, dir, last, func(b []byte) []byte { return append(b, make([]byte, 100)...) })
			synced := false
			h := hooks{sync: func(name string) error {
				if name != path {
					return nil
				}
				synced = true
				if fi, err := os.Stat(path); err != nil || fi.Size() != cutAt {
					t.Errorf("fsync of the segment before the cut: %d bytes, want %d", fi.Size(), cutAt)
				}
				if fail {
					return syscall.EIO
				}
				return nil
			}}
			opts := recoveryOpts(1024)
			opts.Sync = SyncAlways
			l, err := openWithHooks(dir, opts, h)
			if fail {
				if err == nil {
					_ = l.Close()
				}
				if !errors.Is(err, syscall.EIO) || errors.Is(err, ErrCorrupt) {
					t.Errorf("Open with a failing fsync = %v, want EIO and not ErrCorrupt", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer l.Close()
			if !synced {
				t.Error("Open did not fsync the cut segment")
			}
			if hd, _ := l.Head(t.Context()); hd != 30 {
				t.Errorf("Head = %d, want 30", hd)
			}
		})
	}
}

// TestRecoverOneSegment cuts a torn tail in a log that never rolled.
func TestRecoverOneSegment(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, recoveryOpts(1024))
	if err != nil {
		t.Fatal(err)
	}
	es := linkedBlocks(1, 2, 40)
	for _, e := range es {
		mustAppend(t, l, e)
	}
	mustClose(t, l)
	if err := os.Truncate(filepath.Join(dir, firstSegment), 300); err != nil {
		t.Fatal(err)
	}
	l, rec, err := reopen(t, dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	wantEntries(t, l, es, 1)
	if _, warned := rec.find(slog.LevelWarn, "wal torn tail cut"); !warned {
		t.Error("no torn tail warning")
	}
}
