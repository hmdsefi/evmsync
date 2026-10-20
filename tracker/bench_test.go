// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// benchSource answers from a precomputed chain at zero cost: no block has a matching log.
type benchSource struct {
	headers map[common.Hash]chain.Header
}

func (s *benchSource) Latest(context.Context) (chain.Header, error)    { return chain.Header{}, nil }
func (s *benchSource) Finalized(context.Context) (chain.Header, error) { return chain.Header{}, nil }

func (s *benchSource) HeaderByNumber(context.Context, uint64) (chain.Header, error) {
	return chain.Header{}, chain.ErrNotFound
}

func (s *benchSource) HeaderByHash(_ context.Context, h common.Hash) (chain.Header, error) {
	if hd, ok := s.headers[h]; ok {
		return hd, nil
	}
	return chain.Header{}, chain.ErrNotFound
}

func (s *benchSource) Logs(context.Context, common.Hash, chain.Filter) ([]chain.Log, error) {
	return nil, nil
}

func (s *benchSource) LogsInRange(context.Context, uint64, uint64, chain.Filter) ([]chain.Log, error) {
	return nil, nil
}

// benchChain returns n parent-linked refs numbered 0 to n-1 and a source that knows them.
func benchChain(n int) ([]chain.BlockRef, *benchSource) {
	hash := func(i int) common.Hash {
		var h common.Hash
		h[0] = 0xbe
		binary.BigEndian.PutUint64(h[24:], uint64(i)+1) //nolint:gosec // i >= 0
		return h
	}
	refs := make([]chain.BlockRef, n)
	src := &benchSource{headers: make(map[common.Hash]chain.Header, n)}
	for i := range refs {
		refs[i] = chain.BlockRef{Number: uint64(i), Hash: hash(i)}
		if i > 0 {
			refs[i].Parent = refs[i-1].Hash
		}
		src.headers[refs[i].Hash] = chain.Header{Ref: refs[i]}
	}
	return refs, src
}

// benchTracker returns a tracker finalized at refs[0] with refs[1:window+1] in its window.
func benchTracker(tb testing.TB, refs []chain.BlockRef, src chain.Source, window int) *Tracker {
	tb.Helper()
	st := chainlog.NewState(0)
	st.Apply(chainlog.Entry{Kind: chainlog.KindFinalize, To: refs[0]})
	for _, r := range refs[1 : window+1] {
		st.Apply(chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r}})
	}
	return New(src, Config{}, st)
}

// BenchmarkHeadExtend measures Head of the tip's child over a window of 1,000 to 2,000 blocks:
// the tracker is rebuilt with 1,000 blocks, outside the timer, every 1,000 heads.
func BenchmarkHeadExtend(b *testing.B) {
	const window = 1000
	refs, src := benchChain(2*window + 1)
	ctx := b.Context()
	tr := benchTracker(b, refs, src, window)
	next := window + 1
	b.ReportAllocs()
	for b.Loop() {
		if next == len(refs) {
			b.StopTimer()
			tr = benchTracker(b, refs, src, window)
			next = window + 1
			b.StartTimer()
		}
		if _, err := tr.Head(ctx, chain.Header{Ref: refs[next]}); err != nil {
			b.Fatal(err)
		}
		next++
	}
}

// BenchmarkHeadDuplicate measures Head of the tip again.
func BenchmarkHeadDuplicate(b *testing.B) {
	const window = 1000
	refs, src := benchChain(window + 1)
	ctx := b.Context()
	tr := benchTracker(b, refs, src, window)
	h := chain.Header{Ref: refs[window]}
	b.ReportAllocs()
	for b.Loop() {
		if got, err := tr.Head(ctx, h); err != nil || got != nil {
			b.Fatalf("Head(tip) = %v, %v", got, err)
		}
	}
}

// BenchmarkFinalizeSteady measures Finalize of the oldest window block, one block per call. The
// tracker is rebuilt with a full window, outside the timer, once the window is empty, so the
// window shrinks from its full size to zero between rebuilds and compacts along the way.
func BenchmarkFinalizeSteady(b *testing.B) {
	for _, window := range []int{1000, 100_000} {
		b.Run(fmt.Sprintf("window=%d", window), func(b *testing.B) {
			refs, src := benchChain(window + 1)
			ctx := b.Context()
			tr := benchTracker(b, refs, src, window)
			next := 1
			b.ReportAllocs()
			for b.Loop() {
				if next == len(refs) {
					b.StopTimer()
					tr = benchTracker(b, refs, src, window)
					next = 1
					b.StartTimer()
				}
				if _, err := tr.Finalize(ctx, refs[next]); err != nil {
					b.Fatal(err)
				}
				next++
			}
		})
	}
}

// BenchmarkFillWindow fills an empty window with DefaultWindowCap heads, one block each, on one
// tracker. B/op is everything the fill allocates, returned entries included; retained-B is the
// heap the full tracker still holds after a garbage collection.
func BenchmarkFillWindow(b *testing.B) {
	refs, src := benchChain(DefaultWindowCap + 1)
	ctx := b.Context()
	var retained float64
	b.ReportAllocs()
	for b.Loop() {
		var before, after runtime.MemStats
		b.StopTimer()
		runtime.GC()
		runtime.ReadMemStats(&before)
		b.StartTimer()
		tr := benchTracker(b, refs, src, 0)
		for _, r := range refs[1:] {
			if _, err := tr.Head(ctx, chain.Header{Ref: r}); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		runtime.GC()
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(tr)
		retained = float64(after.HeapAlloc) - float64(before.HeapAlloc)
		b.StartTimer()
	}
	b.ReportMetric(retained, "retained-B")
}
