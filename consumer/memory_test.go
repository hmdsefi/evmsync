// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/consumer"
)

// newTraceMemory returns a Memory over the trace state, starting empty, and its trace.
func newTraceMemory() (*consumer.Memory[[]uint64], *trace) {
	tr, apply, clone := newTrace()
	return consumer.NewMemory([]uint64{}, apply, clone), tr
}

// check fails the test unless the trace counts applies and clones, and State returns want with
// exactly one clone and no apply. It then takes that clone off the count, so counts exclude
// State calls.
func check(t *testing.T, m *consumer.Memory[[]uint64], tr *trace, want []uint64, applies, clones int) {
	t.Helper()
	if tr.applies != applies || tr.clones != clones {
		t.Errorf("applies %d, clones %d; want %d, %d", tr.applies, tr.clones, applies, clones)
	}
	got := m.State()
	if !slices.Equal(got, want) {
		t.Errorf("State() = %v, want %v", got, want)
	}
	if tr.applies != applies || tr.clones != clones+1 {
		t.Errorf("State made %d applies and %d clones, want 0 and 1", tr.applies-applies, tr.clones-clones)
	}
	tr.clones--
}

// upTo returns the numbers 1 to n.
func upTo(n uint64) []uint64 {
	var out []uint64
	for i := uint64(1); i <= n; i++ {
		out = append(out, i)
	}
	return out
}

func TestMemoryBlocks(t *testing.T) {
	c, rs := refs(t, 3)
	m, tr := newTraceMemory()
	if tr.applies != 0 || tr.clones != 0 {
		t.Fatalf("NewMemory made %d applies and %d clones, want none", tr.applies, tr.clones)
	}
	applyAll(t, m, fin(rs[0]), blk(c, rs[1]), blk(c, rs[2]), blk(c, rs[3]))
	check(t, m, tr, []uint64{1, 2, 3}, 3, 1)
	if off, err := m.Offset(t.Context()); off != 4 || err != nil {
		t.Errorf("Offset() = %d, %v; want 4, nil", off, err)
	}
}

func TestMemoryRevert(t *testing.T) {
	c, a := refs(t, 5)
	m, tr := newTraceMemory()
	applyAll(t, m, fin(a[0]), blk(c, a[1]), blk(c, a[2]), blk(c, a[3]), blk(c, a[4]), blk(c, a[5]))
	check(t, m, tr, []uint64{1, 2, 3, 4, 5}, 5, 1)
	applyAll(t, m, rev(a[2]))
	check(t, m, tr, []uint64{1, 2}, 5+2, 2)

	b := c.Reorg(3, 2) // B3, B4 on A2
	applyAll(t, m, blk(c, b[0]), blk(c, b[1]))
	check(t, m, tr, []uint64{1, 2, 3, 4}, 9, 2)

	applyAll(t, m, rev(a[0]))
	check(t, m, tr, nil, 9, 2)

	c1 := c.Reorg(4, 1)[0]
	applyAll(t, m, blk(c, c1))
	check(t, m, tr, []uint64{1}, 10, 3)
	applyAll(t, m, rev(c1))
	check(t, m, tr, []uint64{1}, 10, 3)
	if off, _ := m.Offset(t.Context()); off != 12 {
		t.Errorf("Offset() = %d, want 12", off)
	}
}

func TestMemoryRevertToBaseWithoutPending(t *testing.T) {
	_, rs := refs(t, 0)
	m, tr := newTraceMemory()
	applyAll(t, m, fin(rs[0]), rev(rs[0]))
	check(t, m, tr, nil, 0, 0)
}

func TestMemoryFinalize(t *testing.T) {
	c, a := refs(t, 5)
	m, tr := newTraceMemory()
	applyAll(t, m, fin(a[0]), blk(c, a[1]), blk(c, a[2]), blk(c, a[3]), blk(c, a[4]), blk(c, a[5]))
	applyAll(t, m, fin(a[3]))
	check(t, m, tr, []uint64{1, 2, 3, 4, 5}, 5+3, 1)

	applyAll(t, m, rev(a[3])) // the base: the snapshot is moved
	check(t, m, tr, []uint64{1, 2, 3}, 8, 1)

	applyAll(t, m, blk(c, a[4]))
	check(t, m, tr, []uint64{1, 2, 3, 4}, 9, 2)
	applyAll(t, m, fin(a[4]))
	check(t, m, tr, []uint64{1, 2, 3, 4}, 9, 2)

	// fin(A4) left no pending block, so A4 is the base and a revert to it changes nothing.
	applyAll(t, m, rev(a[4]))
	check(t, m, tr, []uint64{1, 2, 3, 4}, 9, 2)
}

func TestMemoryFinalizeThenPartialRevert(t *testing.T) {
	c, a := refs(t, 5)
	m, tr := newTraceMemory()
	applyAll(t, m, fin(a[0]), blk(c, a[1]), blk(c, a[2]), blk(c, a[3]), blk(c, a[4]), blk(c, a[5]), fin(a[2]))
	check(t, m, tr, []uint64{1, 2, 3, 4, 5}, 5+2, 1)
	applyAll(t, m, rev(a[3])) // the snapshot holds A1 and A2; A3 is re-applied
	check(t, m, tr, []uint64{1, 2, 3}, 8, 2)
	applyAll(t, m, rev(a[2]))
	check(t, m, tr, []uint64{1, 2}, 8, 2)
}

// TestMemoryMatchesNumberAndHash checks that a Revert or Finalize target is matched by its
// number and hash only.
func TestMemoryMatchesNumberAndHash(t *testing.T) {
	c, a := refs(t, 3)
	m, tr := newTraceMemory()
	applyAll(t, m, fin(a[0]), blk(c, a[1]), blk(c, a[2]), blk(c, a[3]))
	applyAll(t, m, fin(chain.BlockRef{Number: 1, Hash: a[1].Hash}), rev(chain.BlockRef{Number: 2, Hash: a[2].Hash}))
	check(t, m, tr, []uint64{1, 2}, 3+1+1, 2)
	applyAll(t, m, rev(a[1])) // the base, which holds only number and hash
	check(t, m, tr, []uint64{1}, 5, 2)
}

func TestMemoryRevertCost(t *testing.T) {
	c, a := refs(t, 64)
	m, tr := newTraceMemory()
	applyAll(t, m, chainEntries(c, a, 65)...)
	check(t, m, tr, upTo(64), 64, 1)
	applyAll(t, m, rev(a[61]))
	check(t, m, tr, upTo(61), 64+61, 2)
}

func TestMemoryFinalBlocks(t *testing.T) {
	c, a := refs(t, 10)
	m, tr := newTraceMemory()
	applyAll(t, m, fin(a[0]), fblk(c, a[2]), fblk(c, a[5]))
	check(t, m, tr, []uint64{2, 5}, 2, 0)
	applyAll(t, m, fin(a[9]), blk(c, a[10]))
	check(t, m, tr, []uint64{2, 5, 10}, 3, 1)
	applyAll(t, m, rev(a[9]))
	check(t, m, tr, []uint64{2, 5}, 3, 1)
}

func TestMemoryFinalBlockIsBase(t *testing.T) {
	c, a := refs(t, 6)
	m, tr := newTraceMemory()
	applyAll(t, m, fin(a[0]), fblk(c, a[5]), blk(c, a[6]), rev(a[5]))
	check(t, m, tr, []uint64{5}, 2, 1)
	// A Finalize below the base leaves it: A5 stays the revert target.
	applyAll(t, m, fin(a[0]), blk(c, a[6]), rev(a[5]))
	check(t, m, tr, []uint64{5}, 3, 2)
}

// TestMemoryLongRun keeps the finalized block 5 below the tip over 300 blocks, so the pending
// blocks' backing array is reused many times, then reverts into the window.
func TestMemoryLongRun(t *testing.T) {
	c, a := refs(t, 300)
	m, tr := newTraceMemory()
	applyAll(t, m, fin(a[0]))
	for n := 1; n <= 300; n++ {
		applyAll(t, m, blk(c, a[n]))
		if n > 5 && n%3 == 0 {
			applyAll(t, m, fin(a[n-5]))
		}
	}
	check(t, m, tr, upTo(300), 300+295, 1)
	applyAll(t, m, rev(a[296])) // A296 is the first pending block: the snapshot holds A1 to A295
	check(t, m, tr, upTo(296), 596, 2)
	applyAll(t, m, rev(a[295])) // the base
	check(t, m, tr, upTo(295), 596, 2)
}

func TestMemoryRejects(t *testing.T) {
	c, a := refs(t, 3)
	other := common.Hash{0xaa}
	for _, tt := range []struct {
		name string
		e    chainlog.Entry
		want string
	}{
		{"skipped offset", entryAt(blk(c, a[3]), 5), "offset 5, want 4"},
		{"repeated offset", entryAt(blk(c, a[3]), 3), "offset 3, want 4"},
		{"revert to another hash", entryAt(rev(chain.BlockRef{Number: 1, Hash: other}), 4),
			fmt.Sprintf("revert target 1 %s is not an unfinalized applied block", other.Hex())},
		{"revert to a block never applied", entryAt(rev(a[3]), 4),
			fmt.Sprintf("revert target 3 %s is not an unfinalized applied block", a[3].Hash.Hex())},
		{"revert below the base", entryAt(rev(chain.BlockRef{Number: 0, Hash: other}), 4),
			fmt.Sprintf("revert target 0 %s is not an unfinalized applied block", other.Hex())},
		{"finalize a conflicting block", entryAt(fin(chain.BlockRef{Number: 2, Hash: other}), 4),
			fmt.Sprintf("finalize 2 %s conflicts with applied block %s", other.Hex(), a[2].Hash.Hex())},
		{"unknown kind", chainlog.Entry{Offset: 4, Kind: 7}, "unknown kind 7"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, tr := newTraceMemory()
			applyAll(t, m, fin(a[0]), blk(c, a[1]), blk(c, a[2]))
			err := m.Apply(t.Context(), tt.e)
			if !errors.Is(err, consumer.ErrBadEntry) {
				t.Fatalf("Apply = %v, want an error that Is ErrBadEntry", err)
			}
			if got, want := err.Error(), "consumer: entry does not fit the state: "+tt.want; got != want {
				t.Errorf("Apply = %q, want %q", got, want)
			}
			check(t, m, tr, []uint64{1, 2}, 2, 1)
			if off, _ := m.Offset(t.Context()); off != 3 {
				t.Errorf("Offset() = %d, want 3", off)
			}
			// The rejection left the pending blocks: a revert into them still works.
			applyAll(t, m, rev(a[1]))
			check(t, m, tr, []uint64{1}, 3, 2)
		})
	}
}

// entryAt returns e with offset off.
func entryAt(e chainlog.Entry, off chainlog.Offset) chainlog.Entry {
	e.Offset = off
	return e
}

func TestMemoryConcurrentState(t *testing.T) {
	const n = 10_000
	c, a := refs(t, n)
	log := newLog(t, chainEntries(c, a, n+1)[1:]...) // blk(A1) … blk(A10000)
	apply := func(s map[uint64]uint64, b chain.Block) map[uint64]uint64 {
		s[b.Ref.Number] = b.Ref.Number
		return s
	}
	m := consumer.NewMemory(map[uint64]uint64{}, apply, maps.Clone)

	ctx, cancel := context.WithCancel(bounded(t))
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx, log, &canceler{Consumer: m, at: n, cancel: cancel}) }()
	for i := range 1_000 {
		s := m.State()
		s[uint64(n+1+i)] = 0 // the caller owns the result
	}
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}

	got := m.State()
	if len(got) != n {
		t.Errorf("State() has %d keys, want %d", len(got), n)
	}
	for k := uint64(1); k <= n; k++ {
		if v, ok := got[k]; !ok || v != k {
			t.Fatalf("State()[%d] = %d, %t; want %d, true", k, v, ok, k)
		}
	}
}

func TestNewMemoryPanics(t *testing.T) {
	_, apply, clone := newTrace()
	for _, tt := range []struct {
		name string
		f    func()
		want string
	}{
		{"nil apply", func() { consumer.NewMemory([]uint64{}, nil, clone) }, "evmsync: consumer.NewMemory: nil apply function"},
		{"nil clone", func() { consumer.NewMemory([]uint64{}, apply, nil) }, "evmsync: consumer.NewMemory: nil clone function"},
	} {
		if got := panicText(tt.f); got != tt.want {
			t.Errorf("%s: panic %q, want %q", tt.name, got, tt.want)
		}
	}
}

// panicText returns the value f panics with, formatted with %v, or "" if it returns.
func panicText(f func()) (text string) {
	defer func() {
		if r := recover(); r != nil {
			text = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}
