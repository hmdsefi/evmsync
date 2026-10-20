// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/hmdsefi/evmsync/chainlog"
)

// compactOpts are testOpts with the default window cap and the given sync policy.
func compactOpts(segmentSize int64, sync SyncPolicy) Options {
	o := testOpts(segmentSize)
	o.WindowCap = 0
	o.Sync = sync
	return o
}

// finalizedBlocks returns linked blocks 1..n with one log each and a Finalize of every tenth.
func finalizedBlocks(n uint64) []chainlog.Entry {
	var es []chainlog.Entry
	for _, e := range linkedBlocks(1, n, 40) {
		es = append(es, e)
		if num := e.Block.Ref.Number; num%10 == 0 {
			es = append(es, finalizeTo(ref(num)))
		}
	}
	return es
}

// lastFinalize returns the offset of the newest Finalize in es, numbered from 1.
func lastFinalize(es []chainlog.Entry) chainlog.Offset {
	var l chainlog.Offset
	for i, e := range es {
		if e.Kind == chainlog.KindFinalize {
			l = chainlog.Offset(i + 1) //nolint:gosec // a small test log
		}
	}
	return l
}

func TestCompaction(t *testing.T) {
	ctx := t.Context()
	logger, rec := newRecorder()
	opts := compactOpts(512, SyncAlways)
	opts.Logger = logger
	dir := t.TempDir()
	es := finalizedBlocks(60)
	l := openLog(t, dir, opts, hooks{})
	for _, e := range es {
		mustAppend(t, l, e)
	}
	before := segmentNames(t, dir)
	bound := lastFinalize(es) + 1
	var wantKept []string
	for i, name := range before {
		last := chainlog.Offset(len(es))
		if i < len(before)-1 {
			next, _ := parseSegmentName(before[i+1])
			last = next - 1
		}
		if i == len(before)-1 || last >= bound {
			wantKept = append(wantKept, name)
		}
	}
	if len(wantKept) == len(before) {
		t.Fatal("test setup: no segment lies below the bound")
	}

	if err := l.Truncate(ctx, 1000); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	wantSame(t, "segments after Truncate(1000)", segmentNames(t, dir), wantKept)
	firstKept, _ := parseSegmentName(wantKept[0])
	if o, _ := l.Oldest(ctx); o != firstKept {
		t.Errorf("Oldest = %d, want the first remaining segment's offset %d", o, firstKept)
	}
	b, err := os.ReadFile(filepath.Join(dir, checkpointName))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := decodeCheckpoint(b, 0)
	if err != nil {
		t.Fatalf("decodeCheckpoint: %v", err)
	}
	head, _ := l.Head(ctx)
	if cp.x != head || cp.l != bound-1 {
		t.Errorf("checkpoint at x %d, l %d; want Head %d and the last finalize %d", cp.x, cp.l, head, bound-1)
	}
	attrs, _ := rec.find(slog.LevelInfo, "wal compacted")
	wantSame(t, "wal compacted", attrs, map[string]any{
		"before": uint64(bound), "segments_deleted": int64(len(before) - len(wantKept)), "oldest": uint64(firstKept),
	})
	if _, err := l.Read(ctx, firstKept-1, 1); !errors.Is(err, chainlog.ErrTruncated) {
		t.Errorf("Read below Oldest = %v, want ErrTruncated", err)
	}
	snapshot := snap(t, l)
	mustClose(t, l)

	l = openLog(t, dir, compactOpts(512, SyncAlways), hooks{})
	wantSame(t, "log after reopen", snap(t, l), snapshot)
	mustClose(t, l)
}

// TestCompactionCrash stops compaction after its first deletion, as a crash would, and reopens.
func TestCompactionCrash(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	es := finalizedBlocks(60)
	var deleted []string
	stop := hooks{removed: func(name string) bool {
		deleted = append(deleted, name)
		return false
	}}
	l := openLog(t, dir, compactOpts(512, SyncAlways), stop)
	for _, e := range es {
		mustAppend(t, l, e)
	}
	segments := segmentNames(t, dir)
	head, _ := l.Head(ctx)
	st, _ := l.State(ctx)
	if err := l.Truncate(ctx, 1000); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	mustClose(t, l)
	if len(deleted) != 1 || deleted[0] != segments[0] {
		t.Fatalf("deleted %v before the crash, want only %s", deleted, segments[0])
	}

	l = openLog(t, dir, compactOpts(512, SyncAlways), hooks{})
	second, _ := parseSegmentName(segments[1])
	if o, _ := l.Oldest(ctx); o != second {
		t.Errorf("Oldest after the crash = %d, want %d", o, second)
	}
	if h, _ := l.Head(ctx); h != head {
		t.Errorf("Head after the crash = %d, want %d", h, head)
	}
	got, _ := l.State(ctx)
	wantSame(t, "State after the crash", viewOf(got), viewOf(st))
	es2, err := l.Read(ctx, second, 1000)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := int(head - second + 1); len(es2) != want { //nolint:gosec // a small test log
		t.Errorf("Read returned %d entries, want %d", len(es2), want)
	}
	// Compaction finishes on the next Truncate.
	if err := l.Truncate(ctx, 1000); err != nil {
		t.Fatalf("Truncate after reopen: %v", err)
	}
	names := segmentNames(t, dir)
	first, _ := parseSegmentName(names[0])
	if o, _ := l.Oldest(ctx); o != first || len(names) >= len(segments)-1 {
		t.Errorf("after the second Truncate: Oldest %d, %d segments; want the first remaining segment and fewer than %d", o, len(names), len(segments)-1)
	}
}

// TestCompactionBoundsDisk keeps truncating a long-running log and checks its directory size.
func TestCompactionBoundsDisk(t *testing.T) {
	if testing.Short() {
		t.Skip("appends 200,000 blocks")
	}
	ctx := t.Context()
	dir := t.TempDir()
	opts := compactOpts(65536, SyncNever)
	opts.WindowCap = 1000
	l := openLog(t, dir, opts, hooks{})
	const blocks = 200_000
	for n := uint64(1); n <= blocks; n++ {
		es := linkedBlocks(n, 1, 40)
		if n%100 == 0 {
			es = append(es, finalizeTo(ref(n)))
		}
		head := mustAppend(t, l, es...)
		if n%1000 != 0 {
			continue
		}
		if err := l.Truncate(ctx, head+1); err != nil {
			t.Fatalf("Truncate: %v", err)
		}
		files := listing(t, dir)
		var total int64
		for _, size := range files {
			total += size
		}
		if limit := 4*65536 + files[checkpointName] + 4096; total > limit {
			t.Fatalf("after %d blocks the directory holds %d bytes, more than %d", n, total, limit)
		}
	}
}

// TestTruncateConcurrentRead reads from Oldest while another goroutine appends and truncates:
// every error is ErrTruncated and every entry read equals the one appended.
func TestTruncateConcurrentRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := openLog(t, t.TempDir(), compactOpts(512, SyncNever), hooks{})
	es := finalizedBlocks(1000)
	want := make([][]byte, len(es))
	for i, e := range es {
		e.Offset = chainlog.Offset(i + 1) //nolint:gosec // a small test log
		want[i] = chainlog.Encode(nil, e)
	}

	const readers = 4
	var wg sync.WaitGroup
	errs := make([]error, readers)
	for r := range readers {
		wg.Go(func() {
			for ctx.Err() == nil {
				o, err := l.Oldest(ctx)
				if err != nil {
					return
				}
				got, err := l.Read(ctx, o, 64)
				if errors.Is(err, chainlog.ErrTruncated) || errors.Is(err, context.Canceled) {
					continue
				}
				if err != nil {
					errs[r] = err
					return
				}
				for _, e := range got {
					if !reflect.DeepEqual(chainlog.Encode(nil, e), want[e.Offset-1]) {
						errs[r] = errors.New("an entry read differs from the one appended")
						return
					}
				}
			}
		})
	}
	var appendErr error
	for i, e := range es {
		head, err := l.Append(ctx, e)
		if err != nil {
			appendErr = err
			break
		}
		if i%50 == 49 {
			if err := l.Truncate(ctx, head+1); err != nil {
				appendErr = err
				break
			}
		}
	}
	cancel()
	wg.Wait()
	if appendErr != nil {
		t.Fatalf("Append or Truncate: %v", appendErr)
	}
	for r, err := range errs {
		if err != nil {
			t.Errorf("reader %d: %v", r, err)
		}
	}
	if o, _ := l.Oldest(t.Context()); o == 1 {
		t.Error("Oldest is still 1: nothing was truncated")
	}
}

// TestCompactionBound truncates a log whose newest entries follow its last Finalize: compaction
// honors the caller's bound and the last finalize offset plus 1, and checkpoints at Head.
func TestCompactionBound(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	es := finalizedBlocks(60)
	es = append(es, linkedBlocks(61, 10, 40)...) // no Finalize after block 60
	l := openLog(t, dir, compactOpts(512, SyncNever), hooks{})
	for _, e := range es {
		mustAppend(t, l, e)
	}
	bound := lastFinalize(es) + 1
	names := segmentNames(t, dir)
	firsts := make([]chainlog.Offset, len(names))
	for i, name := range names {
		firsts[i], _ = parseSegmentName(name)
	}

	// A caller bound at a segment's last offset keeps that segment: it holds the entry at the bound.
	if err := l.Truncate(ctx, firsts[2]-1); err != nil {
		t.Fatalf("Truncate(%d): %v", firsts[2]-1, err)
	}
	wantSame(t, "segments after Truncate at a segment's last offset", segmentNames(t, dir), names[1:])

	// A caller bound at a segment's first offset keeps that segment and drops the ones before.
	keep := firsts[2]
	if err := l.Truncate(ctx, keep); err != nil {
		t.Fatalf("Truncate(%d): %v", keep, err)
	}
	if o, _ := l.Oldest(ctx); o != keep {
		t.Errorf("Oldest after Truncate(%d) = %d, want %d", keep, o, keep)
	}
	wantSame(t, "segments after Truncate at a segment's start", segmentNames(t, dir), names[2:])

	// A bound beyond the last finalize offset stops at L+1: the segment holding it stays.
	if err := l.Truncate(ctx, 1000); err != nil {
		t.Fatalf("Truncate(1000): %v", err)
	}
	var wantKept []string
	for i, name := range names {
		last := chainlog.Offset(len(es))
		if i < len(names)-1 {
			last = firsts[i+1] - 1
		}
		if i == len(names)-1 || last >= bound {
			wantKept = append(wantKept, name)
		}
	}
	wantSame(t, "segments after Truncate(1000)", segmentNames(t, dir), wantKept)
	head, _ := l.Head(ctx)
	b, _ := os.ReadFile(filepath.Join(dir, checkpointName))
	cp, err := decodeCheckpoint(b, 0)
	if err != nil || cp.x != head || cp.l != bound-1 {
		t.Errorf("checkpoint x %d, l %d, %v; want Head %d and the last finalize %d", cp.x, cp.l, err, head, bound-1)
	}
	before := snap(t, l)
	mustClose(t, l)
	l = openLog(t, dir, compactOpts(512, SyncNever), hooks{})
	wantSame(t, "log after reopen", snap(t, l), before)
}

// TestCompactionOrder records the fsyncs and deletions of a compaction with SyncAlways: the
// checkpoint is durable before the first deletion, the directory fsync follows the last, and the
// dropped files are closed.
func TestCompactionOrder(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	var events []string
	h := hooks{
		sync:    func(name string) error { events = append(events, "sync "+filepath.Base(name)); return nil },
		removed: func(name string) bool { events = append(events, "remove "+name); return true },
	}
	l := openLog(t, dir, compactOpts(512, SyncAlways), h)
	for _, e := range finalizedBlocks(60) {
		mustAppend(t, l, e)
	}
	names := segmentNames(t, dir)
	firstFile := l.segs[0].f
	events = nil
	if err := l.Truncate(ctx, 1000); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	kept := segmentNames(t, dir)
	want := []string{"sync checkpoint.tmp", "sync " + filepath.Base(dir)}
	for _, name := range names[:len(names)-len(kept)] {
		want = append(want, "remove "+name)
	}
	want = append(want, "sync "+filepath.Base(dir))
	wantSame(t, "fsyncs and deletions of a compaction", events, want)
	if _, err := firstFile.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("a dropped segment's file reads %v, want os.ErrClosed", err)
	}
}
