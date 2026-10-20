// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import "context"

// Log is one chain's canonical log. Implementations are safe for concurrent use; one goroutine
// appends (ingest), any number read.
//
// Every method except Close first returns ErrClosed if the log is closed, then ctx.Err() if ctx
// is done, both unwrapped, and only then checks its arguments. Offsets are dense from Oldest to
// Head and never reused.
type Log interface {
	// Meta returns the log's metadata.
	Meta(ctx context.Context) (Meta, error)
	// Append validates entries in order against the log's State and appends all of them, or none
	// with an error wrapping ErrMalformed. It assigns offsets Head+1, Head+2, ... and returns the
	// last one; with no entries it returns Head.
	Append(ctx context.Context, entries ...Entry) (Offset, error)
	// Read returns up to max entries starting at offset from, in offset order. It returns nil
	// when from is Head+1, an error wrapping ErrTruncated when from is below Oldest, and an error
	// wrapping ErrAhead when from is above Head+1. A from of 0 reads from 1. max must be >= 1.
	Read(ctx context.Context, from Offset, max int) ([]Entry, error)
	// Wait returns nil once Head > after, ctx.Err() if ctx ends first, ErrClosed if the log
	// closes first, and an error wrapping ErrAhead when after > Head.
	Wait(ctx context.Context, after Offset) error
	// Head returns the offset of the newest entry, 0 if none was ever appended.
	Head(ctx context.Context) (Offset, error)
	// Oldest returns the offset of the first retained entry, Head+1 when none is retained.
	Oldest(ctx context.Context) (Offset, error)
	// State returns a copy of the validator state after the newest entry.
	State(ctx context.Context) (State, error)
	// Truncate drops retained entries with offsets below min(before, L+1), where L is the offset
	// of the newest Finalize entry (0 if none). Implementations may keep more (the WAL drops
	// whole segments only). The caller passes the lowest offset any consumer still needs, and
	// never truncates a durable log while a consumer that keeps its state only in memory
	// (consumer.Memory, memstore) is in use: after a restart such a consumer replays the whole
	// log. A log that is itself in memory may be truncated, since a restart loses both.
	Truncate(ctx context.Context, before Offset) error
	// Close releases the log's resources. Later calls of other methods return ErrClosed; later
	// calls of Close return nil.
	Close() error
}
