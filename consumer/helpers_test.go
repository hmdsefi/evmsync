// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/consumer"
	"github.com/hmdsefi/evmsync/internal/fakechain"
)

// recorder is a durable test consumer: its fields survive across Run calls, as a database's
// would. Apply records every call in calls; unless e.Offset is failAt, it then commits: it appends
// e.Offset to applied and sets off. At failAt it returns failErr and commits nothing. It is not
// safe for concurrent use: read its fields only after Run returns.
type recorder struct {
	off     chainlog.Offset
	applied []chainlog.Offset
	calls   []chainlog.Offset
	failAt  chainlog.Offset
	failErr error
}

func (r *recorder) Offset(context.Context) (chainlog.Offset, error) { return r.off, nil }

func (r *recorder) Apply(_ context.Context, e chainlog.Entry) error {
	r.calls = append(r.calls, e.Offset)
	if r.failAt != 0 && e.Offset == r.failAt {
		return r.failErr
	}
	r.applied = append(r.applied, e.Offset)
	r.off = e.Offset
	return nil
}

// canceler wraps a consumer and calls cancel right after its Apply of offset at succeeds.
type canceler struct {
	consumer.Consumer
	at     chainlog.Offset
	cancel context.CancelFunc
}

func (c *canceler) Apply(ctx context.Context, e chainlog.Entry) error {
	err := c.Consumer.Apply(ctx, e)
	if err == nil && e.Offset == c.at {
		c.cancel()
	}
	return err
}

// countingLog wraps a log and records, for each Read, its max in reads and the number of entries
// it returned in got, and the after of each Wait in waits. When waiting is not nil, each Wait
// sends its after on it before waiting, so a test can act once Run is at the tail. It is not safe
// for concurrent use: read its fields only after Run returns, and use waiting to act while Run
// is at the tail.
type countingLog struct {
	chainlog.Log
	reads   []int
	got     []int
	waits   []chainlog.Offset
	waiting chan chainlog.Offset
}

func (l *countingLog) Read(ctx context.Context, from chainlog.Offset, max int) ([]chainlog.Entry, error) {
	es, err := l.Log.Read(ctx, from, max)
	l.reads = append(l.reads, max)
	l.got = append(l.got, len(es))
	return es, err
}

func (l *countingLog) Wait(ctx context.Context, after chainlog.Offset) error {
	l.waits = append(l.waits, after)
	if ctx.Done() == nil {
		return errors.New("countingLog: Wait got a context that never ends")
	}
	if l.waiting != nil {
		select {
		case l.waiting <- after:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return l.Log.Wait(ctx, after)
}

// refs returns a fake chain with seed 1 and default parameters, mined to block n, and the refs of
// its blocks 0 to n: refs[0] is genesis G and refs[i] is block Ai.
func refs(tb testing.TB, n int) (*fakechain.Chain, []chain.BlockRef) {
	tb.Helper()
	c := fakechain.New(1, fakechain.Params{})
	return c, append([]chain.BlockRef{c.Genesis().Ref}, c.Mine(n)...)
}

// blk returns a non-final block entry for x with x's logs that match fakechain.DefaultFilter.
func blk(c *fakechain.Chain, x chain.BlockRef) chainlog.Entry {
	b, ok := c.Block(x.Hash, fakechain.DefaultFilter)
	if !ok {
		panic("consumer_test: unknown block " + x.String())
	}
	return chainlog.Entry{Kind: chainlog.KindBlock, Block: b}
}

// fblk returns blk(c, x) marked final.
func fblk(c *fakechain.Chain, x chain.BlockRef) chainlog.Entry {
	e := blk(c, x)
	e.Final = true
	return e
}

// rev returns a Revert entry to x.
func rev(x chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindRevert, To: x}
}

// fin returns a Finalize entry to x.
func fin(x chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindFinalize, To: x}
}

// chainEntries returns fin(G), blk(A1), …, blk(A<n-1>): n entries over rs from refs.
func chainEntries(c *fakechain.Chain, rs []chain.BlockRef, n int) []chainlog.Entry {
	es := []chainlog.Entry{fin(rs[0])}
	for _, r := range rs[1:n] {
		es = append(es, blk(c, r))
	}
	return es
}

// newLog returns a chainlog.Memory with chain ID 31337 and window cap 100,000 holding entries.
func newLog(tb testing.TB, entries ...chainlog.Entry) *chainlog.Memory {
	tb.Helper()
	l := chainlog.NewMemory(chainlog.Meta{ChainID: 31337}, 100_000)
	if _, err := l.Append(tb.Context(), entries...); err != nil {
		tb.Fatalf("Append: %v", err)
	}
	return l
}

// offsets returns the offsets from to to, inclusive.
func offsets(from, to chainlog.Offset) []chainlog.Offset {
	var out []chainlog.Offset
	for o := from; o <= to; o++ {
		out = append(out, o)
	}
	return out
}

// bounded returns a context that ends 30 seconds after the test starts it, so a Run that should
// return but waits at the tail fails the test with context.DeadlineExceeded instead of hanging.
func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// waitFor returns the after of the next Wait call on log, and fails the test if Run, which sends
// its result on done, returns first.
func waitFor(t *testing.T, log *countingLog, done <-chan error) chainlog.Offset {
	t.Helper()
	select {
	case after := <-log.waiting:
		return after
	case err := <-done:
		t.Fatalf("Run returned %v before it waited at the tail", err)
		return 0
	}
}
