// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/hmdsefi/evmsync/chainlog"
)

const (
	segSuffix  = ".seg"
	nameDigits = 20
	// indexEvery is the gap, in records, between two indexed record positions of a segment.
	indexEvery = 256
	readBuffer = 64 << 10
	recHeader  = 8 // a record's length:uint32le crc:uint32le
)

// segment is one segment file: the records of offsets first..last.
type segment struct {
	name  string
	first chainlog.Offset
	last  chainlog.Offset // first-1 for an empty segment
	size  int64           // bytes of whole records, the end of the last one
	index []int64         // file position of the records first, first+256, first+512, ...
	f     *os.File        // read-only for sealed segments; read-write, appending, for the active one
}

// segmentName returns the file name of the segment whose first record has offset first.
func segmentName(first chainlog.Offset) string {
	s := strconv.FormatUint(uint64(first), 10)
	return strings.Repeat("0", nameDigits-len(s)) + s + segSuffix
}

// parseSegmentName returns the first offset that name gives, or false when name is not a
// segment's: 20 decimal digits and ".seg".
func parseSegmentName(name string) (chainlog.Offset, bool) {
	digits, ok := strings.CutSuffix(name, segSuffix)
	if !ok || len(digits) != nameDigits {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return chainlog.Offset(n), true
}

// readRecord reads the next record from r into *buf and returns it. When the input ends inside
// the record it returns the bytes it has and no error, so chainlog.Decode reports the record as
// incomplete. Only other read errors are returned.
func readRecord(r *bufio.Reader, buf *[]byte) ([]byte, error) {
	b := (*buf)[:0]
	b = append(b, make([]byte, recHeader)...)
	n, err := io.ReadFull(r, b)
	if err != nil {
		return b[:n], eofIsNil(err)
	}
	length := binary.LittleEndian.Uint32(b)
	if length > chainlog.MaxRecordSize {
		return b, nil // Decode rejects the length before it reads the body
	}
	b = append(b, make([]byte, length)...)
	n, err = io.ReadFull(r, b[recHeader:])
	*buf = b
	if err != nil {
		return b[:recHeader+n], eofIsNil(err)
	}
	return b, nil
}

func eofIsNil(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}

// readRange calls fn with each entry of s from offset from to offset to, both within s, in
// order. It starts at the nearest indexed record at or below from and reads with ReadAt, so it
// is safe while the active segment grows. A record that fails to decode, or has the wrong
// offset, is ErrCorrupt; other errors are I/O errors.
func (s segment) readRange(from, to chainlog.Offset, fn func(chainlog.Entry)) error {
	k := (from - s.first) / indexEvery
	pos := s.index[k]
	off := s.first + k*indexEvery
	r := bufio.NewReaderSize(io.NewSectionReader(s.f, pos, s.size-pos), readBuffer)
	var buf []byte
	for ; off <= to; off++ {
		rec, err := readRecord(r, &buf)
		if err != nil {
			return ioErr("read", s.f.Name(), err)
		}
		e, n, err := chainlog.Decode(rec)
		if err != nil {
			return fmt.Errorf("%w: segment %s position %d: %w", ErrCorrupt, s.name, pos, err)
		}
		if e.Offset != off {
			return fmt.Errorf("%w: segment %s position %d: offset %d, want %d", ErrCorrupt, s.name, pos, e.Offset, off)
		}
		if off >= from {
			fn(e)
		}
		pos += int64(n)
	}
	return nil
}
