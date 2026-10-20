// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/consumer"
)

// tenEntryLog returns the log fin(G), blk(A1) … blk(A9).
func tenEntryLog(t *testing.T) *chainlog.Memory {
	t.Helper()
	c, rs := refs(t, 9)
	return newLog(t, chainEntries(c, rs, 10)...)
}

// runTo runs Run over log with c wrapped in a canceler at offset at, and returns Run's error.
func runTo(t *testing.T, log chainlog.Log, c consumer.Consumer, at chainlog.Offset) error {
	t.Helper()
	ctx, cancel := context.WithCancel(bounded(t))
	defer cancel()
	return consumer.Run(ctx, log, &canceler{Consumer: c, at: at, cancel: cancel})
}

func TestRunFromOffset(t *testing.T) {
	r := &recorder{off: 3}
	log := &countingLog{Log: tenEntryLog(t)}
	err := runTo(t, log, r, 10)
	if err != context.Canceled {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if want := offsets(4, 10); !slices.Equal(r.applied, want) {
		t.Errorf("applied = %v, want %v", r.applied, want)
	}
	if len(log.reads) != 1 {
		t.Errorf("Run called Read %d times, want 1: it stops once it sees the context end", len(log.reads))
	}
}

func TestRunBatchesAndWait(t *testing.T) {
	c, rs := refs(t, 649)
	all := chainEntries(c, rs, 650)
	inner := newLog(t, all[:600]...)
	log := &countingLog{Log: inner, waiting: make(chan chainlog.Offset)}
	r := &recorder{}

	done := make(chan error, 1)
	go func() { done <- runTo(t, log, r, 650) }()

	if after := waitFor(t, log, done); after != 600 {
		t.Fatalf("first Wait after %d, want 600", after)
	}
	if _, err := inner.Append(t.Context(), all[600:]...); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}

	for i, max := range log.reads {
		if max != 256 {
			t.Errorf("Read %d had max %d, want 256", i+1, max)
		}
	}
	if len(log.got) < 4 || !slices.Equal(log.got[:4], []int{256, 256, 88, 0}) {
		t.Errorf("Read returned %v entries, want 256, 256, 88, 0 before the first Wait", log.got)
	}
	if want := []chainlog.Offset{600}; !slices.Equal(log.waits, want) {
		t.Errorf("Wait calls after %v, want %v", log.waits, want)
	}
	if want := offsets(1, 650); !slices.Equal(r.applied, want) {
		t.Errorf("applied %d offsets %v…, want 1 … 650", len(r.applied), r.applied[:min(len(r.applied), 5)])
	}
}

// offsetFunc is a consumer whose Offset calls offset and whose Apply fails the test.
type offsetFunc struct {
	t      *testing.T
	offset func(context.Context) (chainlog.Offset, error)
}

func (c offsetFunc) Offset(ctx context.Context) (chainlog.Offset, error) { return c.offset(ctx) }

func (c offsetFunc) Apply(context.Context, chainlog.Entry) error {
	c.t.Error("Apply called")
	return nil
}

func TestRunContext(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(bounded(t))
		cancel()
		log := &countingLog{Log: tenEntryLog(t)}
		c := offsetFunc{t: t, offset: func(context.Context) (chainlog.Offset, error) {
			t.Error("Offset called")
			return 0, nil
		}}
		if err := consumer.Run(ctx, log, c); err != context.Canceled {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
		if len(log.reads) != 0 {
			t.Errorf("Run called Read %d times, want 0", len(log.reads))
		}
	})
	t.Run("offset cancels", func(t *testing.T) {
		ctx, cancel := context.WithCancel(bounded(t))
		defer cancel()
		c := offsetFunc{t: t, offset: func(context.Context) (chainlog.Offset, error) {
			cancel()
			return 0, errors.New("connection reset")
		}}
		if err := consumer.Run(ctx, tenEntryLog(t), c); err != context.Canceled {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		defer cancel()
		err := consumer.Run(ctx, newLog(t), &recorder{})
		if err != context.DeadlineExceeded {
			t.Fatalf("Run = %v, want context.DeadlineExceeded", err)
		}
	})
}

func TestRunOffsetError(t *testing.T) {
	c := offsetFunc{t: t, offset: func(context.Context) (chainlog.Offset, error) {
		return 0, errors.New("connection reset")
	}}
	err := consumer.Run(bounded(t), tenEntryLog(t), c)
	if got, want := errText(err), "consumer: offset: connection reset"; got != want {
		t.Errorf("Run = %q, want %q", got, want)
	}
}

func TestRunHeadError(t *testing.T) {
	log := tenEntryLog(t)
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := consumer.Run(bounded(t), log, &recorder{})
	if !errors.Is(err, chainlog.ErrClosed) {
		t.Errorf("Run = %v, want an error that Is chainlog.ErrClosed", err)
	}
	if got, want := errText(err), "consumer: log head: chainlog: log closed"; got != want {
		t.Errorf("Run = %q, want %q", got, want)
	}

	ctx, cancel := context.WithCancel(bounded(t))
	defer cancel()
	if err := consumer.Run(ctx, headCancelLog{Log: tenEntryLog(t), cancel: cancel}, &recorder{}); err != context.Canceled {
		t.Errorf("Run with a Head that cancels and fails = %v, want context.Canceled", err)
	}
}

// headCancelLog wraps a log whose Head calls cancel and fails.
type headCancelLog struct {
	chainlog.Log
	cancel context.CancelFunc
}

func (l headCancelLog) Head(context.Context) (chainlog.Offset, error) {
	l.cancel()
	return 0, errors.New("connection reset")
}

func TestRunAheadOfLog(t *testing.T) {
	log := &countingLog{Log: tenEntryLog(t)}
	r := &recorder{off: 11}
	err := consumer.Run(bounded(t), log, r)
	if !errors.Is(err, consumer.ErrAheadOfLog) {
		t.Fatalf("Run = %v, want an error that Is ErrAheadOfLog", err)
	}
	if got, want := err.Error(), "consumer: offset is ahead of the log head: offset 11, head 10"; got != want {
		t.Errorf("Run = %q, want %q", got, want)
	}
	if len(log.reads) != 0 || len(r.calls) != 0 {
		t.Errorf("Run called Read %d times and Apply %d times, want 0 and 0", len(log.reads), len(r.calls))
	}
}

func TestRunBehindLog(t *testing.T) {
	c, rs := refs(t, 3)
	log := newLog(t, fin(rs[0]), blk(c, rs[1]), blk(c, rs[2]), fin(rs[2]), blk(c, rs[3]))
	if err := log.Truncate(t.Context(), 4); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	r := &recorder{off: 1}
	err := consumer.Run(bounded(t), log, r)
	if !errors.Is(err, consumer.ErrBehindLog) || !errors.Is(err, chainlog.ErrTruncated) {
		t.Fatalf("Run = %v, want an error that Is ErrBehindLog and chainlog.ErrTruncated", err)
	}
	if prefix := "consumer: offset is behind the oldest log entry: next offset 2: "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("Run = %q, want prefix %q", err, prefix)
	}
	if len(r.calls) != 0 {
		t.Errorf("Apply called for %v, want no call", r.calls)
	}
}

// errLog is a log whose Read fails with err.
type errLog struct {
	chainlog.Log
	err error
}

func (l errLog) Read(context.Context, chainlog.Offset, int) ([]chainlog.Entry, error) {
	return nil, l.err
}

func TestRunReadError(t *testing.T) {
	log := errLog{Log: tenEntryLog(t), err: errors.New("disk read")}
	err := consumer.Run(bounded(t), log, &recorder{off: 2})
	if got, want := errText(err), "consumer: read from offset 3: disk read"; got != want {
		t.Errorf("Run = %q, want %q", got, want)
	}

	for _, readErr := range []error{errors.New("disk read"), fmt.Errorf("%w: read from 1, oldest is 2", chainlog.ErrTruncated)} {
		ctx, cancel := context.WithCancel(bounded(t))
		canceling := cancelLog{Log: errLog{Log: tenEntryLog(t), err: readErr}, cancel: cancel}
		if err := consumer.Run(ctx, canceling, &recorder{}); err != context.Canceled {
			t.Errorf("Run with a Read that cancels and fails with %q = %v, want context.Canceled", readErr, err)
		}
		cancel()
	}
}

// cancelLog wraps a log and calls cancel before each Read.
type cancelLog struct {
	chainlog.Log
	cancel context.CancelFunc
}

func (l cancelLog) Read(ctx context.Context, from chainlog.Offset, max int) ([]chainlog.Entry, error) {
	l.cancel()
	return l.Log.Read(ctx, from, max)
}

func TestRunApplyError(t *testing.T) {
	log := tenEntryLog(t)
	diskFull := errors.New("disk full")
	r := &recorder{failAt: 6, failErr: diskFull}
	err := runTo(t, log, r, 10)

	var ae *consumer.ApplyError
	if !errors.As(err, &ae) {
		t.Fatalf("Run = %v, want an *ApplyError", err)
	}
	if ae.Offset != 6 || ae.Kind != chainlog.KindBlock || ae.Err != diskFull {
		t.Errorf("ApplyError = %+v, want offset 6, kind block, Err disk full", *ae)
	}
	if got, want := err.Error(), "consumer: apply offset 6 (block): disk full"; got != want {
		t.Errorf("Run = %q, want %q", got, want)
	}
	if want := offsets(1, 6); !slices.Equal(r.calls, want) {
		t.Errorf("Apply calls %v, want %v", r.calls, want)
	}
	if off, _ := r.Offset(t.Context()); off != 5 {
		t.Errorf("Offset = %d, want 5", off)
	}

	r.failAt = 0
	if err := runTo(t, log, r, 10); err != context.Canceled {
		t.Fatalf("second Run = %v, want context.Canceled", err)
	}
	if want := offsets(1, 10); !slices.Equal(r.applied, want) {
		t.Errorf("applied = %v, want %v", r.applied, want)
	}
}

func TestRunApplyErrorKinds(t *testing.T) {
	c, rs := refs(t, 2)
	log := newLog(t, fin(rs[0]), blk(c, rs[1]), blk(c, rs[2]), rev(rs[1]), fin(rs[1]), fblk(c, rs[2]))
	for _, tt := range []struct {
		at   chainlog.Offset
		want string
	}{
		{4, "consumer: apply offset 4 (revert): boom"},
		{5, "consumer: apply offset 5 (finalize): boom"},
		{6, "consumer: apply offset 6 (block): boom"},
	} {
		r := &recorder{failAt: tt.at, failErr: errors.New("boom")}
		err := consumer.Run(bounded(t), log, r)
		if got := errText(err); got != tt.want {
			t.Errorf("Run failing at %d = %q, want %q", tt.at, got, tt.want)
		}
		if want := offsets(1, tt.at-1); !slices.Equal(r.applied, want) {
			t.Errorf("Run failing at %d applied %v, want %v", tt.at, r.applied, want)
		}
	}
}

// cancelApply is a consumer at offset 0 whose Apply calls cancel and returns ctx.Err().
type cancelApply struct{ cancel context.CancelFunc }

func (cancelApply) Offset(context.Context) (chainlog.Offset, error) { return 0, nil }

func (c cancelApply) Apply(ctx context.Context, _ chainlog.Entry) error {
	c.cancel()
	return ctx.Err()
}

func TestRunApplyErrorWrapsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(bounded(t))
	defer cancel()
	err := consumer.Run(ctx, tenEntryLog(t), cancelApply{cancel: cancel})
	var ae *consumer.ApplyError
	if err == context.Canceled || !errors.As(err, &ae) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want an *ApplyError wrapping context.Canceled", err)
	}
	if got, want := err.Error(), "consumer: apply offset 1 (finalize): context canceled"; got != want {
		t.Errorf("Run = %q, want %q", got, want)
	}
}

func TestApplyErrorText(t *testing.T) {
	inner := errors.New("boom")
	for _, tt := range []struct {
		kind chainlog.Kind
		want string
	}{
		{chainlog.KindBlock, "consumer: apply offset 7 (block): boom"},
		{chainlog.KindRevert, "consumer: apply offset 7 (revert): boom"},
		{chainlog.KindFinalize, "consumer: apply offset 7 (finalize): boom"},
		{9, "consumer: apply offset 7 (kind 9): boom"},
		{0, "consumer: apply offset 7 (kind 0): boom"},
	} {
		e := &consumer.ApplyError{Offset: 7, Kind: tt.kind, Err: inner}
		if got := e.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
		if got := errors.Unwrap(e); got != inner {
			t.Errorf("Unwrap() = %v, want %v", got, inner)
		}
	}
}

// gapLog wraps a log whose Read from offset 4 returns the entry with offset 4 and then the one
// with offset second.
type gapLog struct {
	chainlog.Log
	second chainlog.Offset
}

func (l gapLog) Read(ctx context.Context, from chainlog.Offset, max int) ([]chainlog.Entry, error) {
	es, err := l.Log.Read(ctx, from, max)
	if from == 4 && err == nil && len(es) >= 3 {
		es = []chainlog.Entry{es[0], es[l.second-4]}
	}
	return es, err
}

func TestRunOffsetGap(t *testing.T) {
	for _, second := range []chainlog.Offset{6, 4} {
		r := &recorder{off: 3}
		err := consumer.Run(bounded(t), gapLog{Log: tenEntryLog(t), second: second}, r)
		if !errors.Is(err, consumer.ErrOffsetGap) {
			t.Fatalf("Run with offsets 4, %d = %v, want an error that Is ErrOffsetGap", second, err)
		}
		if got, want := err.Error(), fmt.Sprintf("consumer: log offsets are not contiguous: got offset %d, want 5", second); got != want {
			t.Errorf("Run = %q, want %q", got, want)
		}
		if want := []chainlog.Offset{4}; !slices.Equal(r.calls, want) {
			t.Errorf("Run with offsets 4, %d: Apply calls %v, want %v", second, r.calls, want)
		}
	}
}

func TestRunRestart(t *testing.T) {
	c, rs := refs(t, 11)
	log := newLog(t, chainEntries(c, rs, 12)...)
	r := &recorder{}

	if err := runTo(t, log, r, 3); err != context.Canceled {
		t.Fatalf("Run to 3 = %v, want context.Canceled", err)
	}
	ctx, cancel := context.WithCancel(bounded(t))
	cancel()
	if err := consumer.Run(ctx, log, r); err != context.Canceled {
		t.Fatalf("Run with a canceled context = %v, want context.Canceled", err)
	}
	for _, at := range []chainlog.Offset{7, 12} {
		if err := runTo(t, log, r, at); err != context.Canceled {
			t.Fatalf("Run to %d = %v, want context.Canceled", at, err)
		}
	}
	if want := offsets(1, 12); !slices.Equal(r.applied, want) {
		t.Errorf("applied = %v, want %v", r.applied, want)
	}

	restored := &recorder{off: 7}
	if err := runTo(t, log, restored, 12); err != context.Canceled {
		t.Fatalf("Run from 7 = %v, want context.Canceled", err)
	}
	if want := offsets(8, 12); !slices.Equal(restored.applied, want) {
		t.Errorf("restored applied = %v, want %v", restored.applied, want)
	}
}

func TestRunLogClosed(t *testing.T) {
	inner := tenEntryLog(t)
	log := &countingLog{Log: inner, waiting: make(chan chainlog.Offset)}
	r := &recorder{}

	done := make(chan error, 1)
	go func() { done <- consumer.Run(bounded(t), log, r) }()

	if after := waitFor(t, log, done); after != 10 {
		t.Fatalf("Wait after %d, want 10", after)
	}
	if err := inner.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := <-done
	if !errors.Is(err, chainlog.ErrClosed) {
		t.Fatalf("Run = %v, want an error that Is chainlog.ErrClosed", err)
	}
	if got, want := err.Error(), "consumer: wait after offset 10: chainlog: log closed"; got != want {
		t.Errorf("Run = %q, want %q", got, want)
	}
	if want := offsets(1, 10); !slices.Equal(r.applied, want) {
		t.Errorf("applied = %v, want %v", r.applied, want)
	}
}

func TestRunWaitCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(bounded(t))
	log := &countingLog{Log: tenEntryLog(t), waiting: make(chan chainlog.Offset)}
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx, log, &recorder{}) }()
	waitFor(t, log, done)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
}

// errText returns err's text, or "<nil>".
func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
