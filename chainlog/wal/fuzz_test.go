// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hmdsefi/evmsync/chainlog"
)

// FuzzRecover damages a closed log and reopens it. Open must never panic and may fail only with
// ErrCorrupt; whatever it keeps must be a prefix of what was appended. Mode 0 cuts the last
// segment, which Open always recovers; modes 1 and 2 flip a byte of the last or of any segment,
// which is corruption unless it hits the very last record.
func FuzzRecover(f *testing.F) {
	f.Add(uint8(4), uint8(0), uint32(100), uint8(0))
	f.Add(uint8(4), uint8(1), uint32(20), uint8(1))
	f.Add(uint8(39), uint8(2), uint32(7), uint8(0x80))
	f.Add(uint8(0), uint8(0), uint32(0), uint8(0))

	f.Fuzz(func(t *testing.T, n, mode uint8, pos uint32, x uint8) {
		dir := t.TempDir()
		es := linkedBlocks(1, 1+uint64(n%40), 40)
		l, err := Open(dir, recoveryOpts(512))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		for _, e := range es {
			mustAppend(t, l, e)
		}
		mustClose(t, l)

		names := segmentNames(t, dir)
		seg := len(names) - 1
		if mode%3 == 2 {
			seg = int(pos % uint32(len(names))) //nolint:gosec // fewer than 40 segments
		}
		path := filepath.Join(dir, names[seg])
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		size := uint32(len(b)) //nolint:gosec // a segment of at most a few KiB
		first, _ := parseSegmentName(names[seg])
		start, end := index(first)-1, len(es) // the segment's entries are es[start:end]
		if seg < len(names)-1 {
			next, _ := parseSegmentName(names[seg+1])
			end = index(next) - 1
		}
		ends := recordEnds(es[start:end], first)

		var wantHead int // -1: any prefix; -2: ErrCorrupt
		switch {
		case mode%3 == 0:
			cut := int64(pos % (size + 1))
			if err := os.Truncate(path, cut); err != nil {
				t.Fatal(err)
			}
			kept := 0
			for kept < len(ends) && ends[kept] <= cut {
				kept++
			}
			wantHead = start + kept
		case size == 0:
			t.Skip("no byte to flip")
		default:
			i := pos % size
			b[i] ^= x | 1
			if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // a segment of the test's own directory
				t.Fatal(err)
			}
			wantHead = -1
			recStart := int64(0)
			for k, recEnd := range ends {
				if int64(i) < recEnd {
					switch lastRecord := seg == len(names)-1 && k == len(ends)-1; {
					case int64(i) < recStart+4: // the length field: no promise beyond the prefix
					case lastRecord:
						wantHead = len(es) - 1
					default:
						wantHead = -2
					}
					break
				}
				recStart = recEnd
			}
		}

		l, err = Open(dir, recoveryOpts(512))
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Open = %v, want success or ErrCorrupt", err)
			}
			if wantHead >= 0 {
				t.Fatalf("Open = %v, want Head %d", err, wantHead)
			}
			return
		}
		defer l.Close()
		if wantHead == -2 {
			t.Fatal("Open succeeded on a damaged record that is not the last one, want ErrCorrupt")
		}
		head, _ := l.Head(t.Context())
		if wantHead >= 0 && index(head) != wantHead {
			t.Fatalf("Head = %d, want %d", head, wantHead)
		}
		if head == 0 {
			return
		}
		got, err := l.Read(t.Context(), 1, len(es))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !bytes.Equal(encoded(got), encoded(es[:head])) {
			t.Fatal("entries 1..Head differ from the originals")
		}
	})
}

// index returns offset o as an int; test logs hold far fewer than 2^63 entries.
func index(o chainlog.Offset) int { return int(o) } //nolint:gosec // a small test offset
