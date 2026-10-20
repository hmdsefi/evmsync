// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

func TestMemoryMeta(t *testing.T) {
	ctx := t.Context()
	topic := common.Hash{31: 7}
	addrs := []common.Address{}
	topics := [][]common.Hash{{}, {topic}}
	m := NewMemory(Meta{ChainID: 1, Filter: chain.Filter{Addresses: addrs, Topics: topics}}, 0)
	topics[1][0] = common.Hash{1} // the log keeps its own copy
	got, err := m.Meta(ctx)
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if got.ID.IsZero() {
		t.Error("Meta().ID is zero, want a random ID")
	}
	if got.ChainID != 1 || got.Filter.Addresses != nil || !reflect.DeepEqual(got.Filter.Topics, [][]common.Hash{nil, {topic}}) {
		t.Errorf("Meta() = %+v, want ChainID 1, nil Addresses, Topics [nil [topic]]", got)
	}

	other, _ := NewMemory(Meta{}, 0).Meta(ctx)
	if other.ID == got.ID {
		t.Errorf("two logs created with a zero ID share ID %s", got.ID)
	}
	id := ID{1, 2, 3}
	kept, _ := NewMemory(Meta{ID: id}, 0).Meta(ctx)
	if kept.ID != id {
		t.Errorf("Meta().ID = %s, want the given %s", kept.ID, id)
	}

	five := Meta{Filter: chain.Filter{Topics: make([][]common.Hash, 5)}}
	if got, want := panicText(func() { NewMemory(five, 0) }), "evmsync: chainlog.NewMemory: chain: invalid filter: 5 topic positions, at most 4"; got != want {
		t.Errorf("NewMemory with 5 topic positions: panic %q, want %q", got, want)
	}
	if got, want := panicText(func() { NewMemory(Meta{}, -1) }), "evmsync: chainlog.NewMemory: cap must not be negative, got -1"; got != want {
		t.Errorf("NewMemory with cap -1: panic %q, want %q", got, want)
	}
}

func TestMemoryTruncateExact(t *testing.T) {
	ctx := t.Context()
	m := NewMemory(Meta{}, 0)
	in := append(blocks(1, 10), finalize(blk(8, 0)))
	in = append(in, blocks(11, 12)...)
	if _, err := m.Append(ctx, in...); err != nil {
		t.Fatalf("Append: %v", err)
	}
	all := m.recs // the backing store before any Truncate

	if err := m.Truncate(ctx, 6); err != nil {
		t.Fatalf("Truncate(6): %v", err)
	}
	if o, _ := m.Oldest(ctx); o != 6 {
		t.Errorf("Oldest after Truncate(6) = %d, want 6", o)
	}
	if err := m.Truncate(ctx, 100); err != nil {
		t.Fatalf("Truncate(100): %v", err)
	}
	if o, _ := m.Oldest(ctx); o != 12 {
		t.Errorf("Oldest after Truncate(100) = %d, want 12 (the Finalize is offset 11)", o)
	}
	_, err := m.Read(ctx, 11, 1)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("Read(11, 1) = %v, want one wrapping ErrTruncated", err)
	}
	if got, want := err.Error(), "chainlog: offset truncated: read from 11, oldest is 12"; got != want {
		t.Errorf("Read(11, 1) error = %q, want %q", got, want)
	}

	if uint64(len(m.recs)) != uint64(m.head-m.oldest)+1 {
		t.Errorf("backing store holds %d records, want Head - Oldest + 1 = %d", len(m.recs), m.head-m.oldest+1)
	}
	for i, rec := range all[:11] {
		if rec != nil {
			t.Errorf("dropped record %d is still referenced", i+1)
		}
	}
	for i, rec := range all[11:] {
		if rec == nil {
			t.Errorf("retained record %d was cleared", i+12)
		}
	}
}

func TestMemoryClosedReleases(t *testing.T) {
	m := NewMemory(Meta{}, 0)
	if _, err := m.Append(t.Context(), blocks(1, 3)...); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if m.recs != nil {
		t.Errorf("Close kept %d records", len(m.recs))
	}
}

// TestAppendCostFlat compares the cost of Append with a window of 99,000 refs and of 10 refs. Each
// of the 1,000 steps appends a block and a Finalize of the window's first ref, so the window keeps
// its length.
func TestAppendCostFlat(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector distorts the timing")
	}
	cost := func(window uint64) time.Duration {
		ctx := t.Context()
		m := NewMemory(Meta{}, 0)
		if _, err := m.Append(ctx, blocks(1, window)...); err != nil {
			t.Fatalf("Append: %v", err)
		}
		next, first := window+1, uint64(1)
		best := time.Duration(-1)
		for range 3 {
			start := time.Now()
			for range 1000 {
				// One block in, the window's first ref finalized: the window keeps its length.
				if _, err := m.Append(ctx, block(blk(next, 0))); err != nil {
					t.Fatalf("Append: %v", err)
				}
				if _, err := m.Append(ctx, finalize(blk(first, 0))); err != nil {
					t.Fatalf("Append: %v", err)
				}
				next++
				first++
			}
			if d := time.Since(start); best < 0 || d < best {
				best = d
			}
		}
		return best
	}
	small, large := cost(10), cost(99_000)
	if ratio := float64(large) / float64(small); ratio > 2 {
		t.Errorf("1,000 appends: %v with a window of 99,000, %v with 10; ratio %.2f, want at most 2", large, small, ratio)
	}
}
