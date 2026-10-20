// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/hmdsefi/evmsync/chainlog"
)

// Consumer applies canonical-log entries in offset order and commits each entry's offset
// atomically with the state change it causes.
type Consumer interface {
	// Offset returns the committed offset: the offset of the last entry whose effects are
	// committed, or 0 if none.
	Offset(ctx context.Context) (chainlog.Offset, error)

	// Apply applies e, whose offset is the committed offset plus one, and commits e.Offset in
	// the same atomic step as the state change. On error nothing of e is committed and Offset is
	// unchanged. Apply must not modify e; it may retain it.
	Apply(ctx context.Context, e chainlog.Entry) error
}

var (
	// ErrBehindLog reports that the next offset was truncated from the log.
	ErrBehindLog = errors.New("consumer: offset is behind the oldest log entry")
	// ErrAheadOfLog reports a committed offset above the log's head.
	ErrAheadOfLog = errors.New("consumer: offset is ahead of the log head")
	// ErrOffsetGap reports that the log returned an entry other than the next offset.
	ErrOffsetGap = errors.New("consumer: log offsets are not contiguous")
	// ErrBadEntry reports an entry that does not fit Memory's state.
	ErrBadEntry = errors.New("consumer: entry does not fit the state")
)

// ApplyError is the error Run returns when Apply fails.
type ApplyError struct {
	Offset chainlog.Offset // the entry's offset
	Kind   chainlog.Kind   // the entry's kind
	Err    error           // what Apply returned
}

// Error returns "consumer: apply offset <offset> (<kind>): <Err>", where <kind> is "block",
// "revert", "finalize" or "kind <n>" for any other value.
func (e *ApplyError) Error() string {
	var kind string
	switch e.Kind {
	case chainlog.KindBlock, chainlog.KindRevert, chainlog.KindFinalize:
		kind = e.Kind.String()
	default:
		kind = "kind " + strconv.Itoa(int(e.Kind))
	}
	return fmt.Sprintf("consumer: apply offset %d (%s): %v", e.Offset, kind, e.Err)
}

// Unwrap returns e.Err.
func (e *ApplyError) Unwrap() error { return e.Err }

// readBatch is the most entries Run reads at once.
const readBatch = 256

// Run feeds c the entries of log from c's committed offset plus one, in order, one Apply per
// entry. It reads in batches, waits at the tail with log.Wait and returns only on error:
// ctx.Err() when ctx ends, an *ApplyError when Apply fails, or a wrapped error from the log or
// from c.Offset. Run retries nothing and starts no goroutine.
//
// Run returns an error wrapping ErrAheadOfLog when c's offset is above the log's head, one
// wrapping both ErrBehindLog and chainlog.ErrTruncated when the log no longer holds the next
// offset, and one wrapping ErrOffsetGap when the log returns an offset other than the next one.
func Run(ctx context.Context, log chainlog.Log, c Consumer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	off, err := c.Offset(ctx)
	if err != nil {
		return orDone(ctx, fmt.Errorf("consumer: offset: %w", err))
	}
	head, err := log.Head(ctx)
	if err != nil {
		return orDone(ctx, fmt.Errorf("consumer: log head: %w", err))
	}
	if off > head {
		return fmt.Errorf("%w: offset %d, head %d", ErrAheadOfLog, off, head)
	}
	next := off + 1
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		es, err := log.Read(ctx, next, readBatch)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(err, chainlog.ErrTruncated) {
				return fmt.Errorf("%w: next offset %d: %w", ErrBehindLog, next, err)
			}
			return fmt.Errorf("consumer: read from offset %d: %w", next, err)
		}
		if len(es) == 0 {
			if err := log.Wait(ctx, next-1); err != nil {
				return orDone(ctx, fmt.Errorf("consumer: wait after offset %d: %w", next-1, err))
			}
			continue
		}
		for i := range es {
			e := &es[i]
			if e.Offset != next {
				return fmt.Errorf("%w: got offset %d, want %d", ErrOffsetGap, e.Offset, next)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := c.Apply(ctx, *e); err != nil {
				return &ApplyError{Offset: e.Offset, Kind: e.Kind, Err: err}
			}
			next++
		}
	}
}

// orDone returns ctx.Err() if ctx has ended, and err otherwise.
func orDone(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}
