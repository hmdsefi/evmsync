// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import "errors"

var (
	// ErrMalformed is returned by State.Check and Log.Append for an entry that may not follow
	// the entries before it.
	ErrMalformed = errors.New("chainlog: malformed entry")
	// ErrClosed is returned by every Log method except Close after the log is closed.
	ErrClosed = errors.New("chainlog: log closed")
	// ErrTruncated is returned by Log.Read for an offset below Oldest.
	ErrTruncated = errors.New("chainlog: offset truncated")
	// ErrAhead is returned by Log.Read and Log.Wait for an offset beyond the head: the caller is
	// reading a different log than the one it committed against.
	ErrAhead = errors.New("chainlog: offset beyond head")
	// ErrBadState is returned by State.UnmarshalBinary for malformed input.
	ErrBadState = errors.New("chainlog: bad state encoding")
)

// Decode returns one of these errors, wrapped with a detail, for each way a record can fail.
var (
	// ErrIncomplete means the input ends before the record does.
	ErrIncomplete = errors.New("chainlog: incomplete record")
	// ErrRecordTooLarge means the record's length is above MaxRecordSize.
	ErrRecordTooLarge = errors.New("chainlog: record too large")
	// ErrChecksum means the record's CRC-32C does not match its body.
	ErrChecksum = errors.New("chainlog: record checksum mismatch")
	// ErrVersion means the record has a version other than 1.
	ErrVersion = errors.New("chainlog: unknown record version")
	// ErrKind means the record's entry kind is not KindBlock, KindRevert or KindFinalize.
	ErrKind = errors.New("chainlog: unknown entry kind")
	// ErrPayload means a field of the record's body is cut short or out of range.
	ErrPayload = errors.New("chainlog: bad record payload")
	// ErrTrailing means bytes remain in the record's body after its payload.
	ErrTrailing = errors.New("chainlog: trailing bytes in record")
)
