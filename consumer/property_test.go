// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/consumer"
	"github.com/hmdsefi/evmsync/internal/fakechain"
	"github.com/hmdsefi/evmsync/tracker"
)

// trackerRun drives a tracker over a faulty fake chain for seed, as the tracker's mutual
// consistency property does: 300 iterations of Step, Head of Latest and Finalize of Finalized,
// tolerating only not-found answers. It appends every result to a log and calls onEntries with
// each non-empty result, offsets set, right after its Append. It returns the log.
func trackerRun(t *testing.T, seed uint64, onEntries func([]chainlog.Entry)) *chainlog.Memory {
	t.Helper()
	ctx := t.Context()
	c := fakechain.New(seed, fakechain.Params{})
	src := fakechain.NewSource(c, seed, fakechain.Faults{
		StaleHeadPPM:   100_000,
		StaleOrphanPPM: 100_000,
		DropOrphans:    seed%2 == 0,
		InterleavePPM:  100_000,
	})
	lg := chainlog.NewMemory(c.Meta(fakechain.DefaultFilter), tracker.DefaultWindowCap)
	tr := tracker.New(src, tracker.Config{Filter: fakechain.DefaultFilter},
		chainlog.NewState(tracker.DefaultWindowCap))
	add := func(es []chainlog.Entry, err error) {
		t.Helper()
		if err != nil {
			if !errors.Is(err, chain.ErrNotFound) {
				t.Fatalf("seed %d: %v", seed, err)
			}
			return
		}
		if len(es) == 0 {
			return
		}
		last, err := lg.Append(ctx, es...)
		if err != nil {
			t.Fatalf("seed %d: Append: %v", seed, err)
		}
		for i := range es {
			es[i].Offset = last - chainlog.Offset(len(es)-1-i) //nolint:gosec // i < len(es)
		}
		if onEntries != nil {
			onEntries(es)
		}
	}
	add(tr.Finalize(ctx, c.Genesis().Ref))
	for range 300 {
		src.Do(func(c *fakechain.Chain) { c.Step() })
		h, err := src.Latest(ctx)
		if err != nil {
			t.Fatal(err)
		}
		add(tr.Head(ctx, h))
		f, err := src.Finalized(ctx)
		if err != nil {
			t.Fatal(err)
		}
		add(tr.Finalize(ctx, f.Ref))
	}
	return lg
}

// pendingModel tracks what Memory keeps unfinalized, to count the clones Memory's contract
// allows: one for a non-final block with nothing pending, one for a revert that keeps at least one
// pending block and removes at least one, none otherwise.
type pendingModel struct {
	pending []chain.BlockRef
	base    chain.BlockRef
	clones  int
}

func (m *pendingModel) apply(t *testing.T, e chainlog.Entry) {
	t.Helper()
	switch e.Kind {
	case chainlog.KindBlock:
		switch {
		case len(m.pending) == 0 && e.Final:
			m.base = e.Block.Ref
		case len(m.pending) == 0:
			m.clones++
			m.pending = append(m.pending, e.Block.Ref)
		default:
			m.pending = append(m.pending, e.Block.Ref)
		}
	case chainlog.KindRevert:
		if e.To == m.base {
			m.pending = m.pending[:0]
			return
		}
		i := slices.Index(m.pending, e.To)
		if i < 0 {
			t.Fatalf("offset %d: revert to %s, which is neither pending nor the base", e.Offset, e.To)
		}
		if i < len(m.pending)-1 {
			m.clones++
		}
		m.pending = m.pending[:i+1]
	case chainlog.KindFinalize:
		k := 0
		for k < len(m.pending) && m.pending[k].Number <= e.To.Number {
			k++
		}
		m.pending = m.pending[k:]
		if e.To.Number >= m.base.Number {
			m.base = e.To
		}
	}
}

// liveNumbers replays entries structurally: a Block pushes its ref, a Revert pops down to its
// target, which must then be the top. It returns the numbers of the live blocks above g.
func liveNumbers(t *testing.T, stack *[]chain.BlockRef, es []chainlog.Entry) []uint64 {
	t.Helper()
	for _, e := range es {
		switch e.Kind {
		case chainlog.KindBlock:
			*stack = append(*stack, e.Block.Ref)
		case chainlog.KindRevert:
			s := *stack
			for s[len(s)-1].Number > e.To.Number {
				s = s[:len(s)-1]
			}
			if s[len(s)-1] != e.To {
				t.Fatalf("offset %d: revert target %s is not on the stack", e.Offset, e.To)
			}
			*stack = s
		}
	}
	nums := []uint64{}
	for _, r := range (*stack)[1:] {
		nums = append(nums, r.Number)
	}
	return nums
}

func TestMemoryEquivalence(t *testing.T) {
	seeds := uint64(200)
	if testing.Short() {
		seeds = 30
	}
	var reverts, partial atomic.Int64
	t.Run("seeds", func(t *testing.T) {
		for seed := uint64(1); seed <= seeds; seed++ {
			t.Run("seed="+strconv.FormatUint(seed, 10), func(t *testing.T) {
				t.Parallel()
				r, p := equivalence(t, seed)
				reverts.Add(r)
				partial.Add(p)
			})
		}
	})
	if reverts.Load() == 0 || partial.Load() == 0 {
		t.Errorf("%d reverts, %d of them partial; the property covers no revert case", reverts.Load(), partial.Load())
	}
}

// equivalence applies seed's tracker entries to a Memory one by one and checks its state and
// clone count after each. It returns the number of reverts and of partial reverts.
func equivalence(t *testing.T, seed uint64) (reverts, partial int64) {
	t.Helper()
	tr, apply, clone := newTrace()
	m := consumer.NewMemory([]uint64{}, apply, clone)
	model := &pendingModel{}
	var stack []chain.BlockRef
	states := 0
	trackerRun(t, seed, func(es []chainlog.Entry) {
		if stack == nil {
			stack = []chain.BlockRef{es[0].To} // the first entry finalizes genesis
		}
		for _, e := range es {
			before := model.clones
			if err := m.Apply(t.Context(), e); err != nil {
				t.Fatalf("seed %d: Apply offset %d (%s): %v", seed, e.Offset, e.Kind, err)
			}
			model.apply(t, e)
			if e.Kind == chainlog.KindRevert {
				reverts++
				if model.clones > before {
					partial++
				}
			}
			want := liveNumbers(t, &stack, []chainlog.Entry{e})
			got := m.State()
			states++
			if !slices.Equal(got, want) {
				t.Fatalf("seed %d offset %d (%s): State() = %v, want %v", seed, e.Offset, e.Kind, got, want)
			}
			if tr.clones-states != model.clones {
				t.Fatalf("seed %d offset %d (%s): %d clones besides State, want %d",
					seed, e.Offset, e.Kind, tr.clones-states, model.clones)
			}
			if off, _ := m.Offset(t.Context()); off != e.Offset {
				t.Fatalf("seed %d: Offset() = %d after applying %d", seed, off, e.Offset)
			}
		}
	})
	return reverts, partial
}

func TestMemoryRestartReplays(t *testing.T) {
	lg := trackerRun(t, 7, nil)
	head, err := lg.Head(t.Context())
	if err != nil || head < 100 {
		t.Fatalf("seed 7 log head %d, %v; want a long log", head, err)
	}
	runToHead := func() *consumer.Memory[[]uint64] {
		_, apply, clone := newTrace()
		m := consumer.NewMemory([]uint64{}, apply, clone)
		ctx, cancel := context.WithCancel(bounded(t))
		defer cancel()
		err := consumer.Run(ctx, lg, &canceler{Consumer: m, at: head, cancel: cancel})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled at the head", err)
		}
		if off, _ := m.Offset(t.Context()); off != head {
			t.Fatalf("Offset() = %d after Run, want %d", off, head)
		}
		return m
	}
	first := runToHead()
	second := runToHead()
	if a, b := first.State(), second.State(); !slices.Equal(a, b) || len(a) == 0 {
		t.Errorf("restarted Memory state %v, first run %v", b, a)
	}
}
