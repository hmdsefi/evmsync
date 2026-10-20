// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

func TestHonestSource(t *testing.T) {
	ctx := t.Context()
	c := New(2, Params{})
	c.Mine(20)
	var orphans []chain.BlockRef
	for n := uint64(18); n <= 20; n++ {
		h, _ := c.Canonical(n)
		orphans = append(orphans, h.Ref)
	}
	c.Reorg(3, 3)
	src := NewSource(c, 2, Faults{})

	var want []Call
	head, err := src.Latest(ctx)
	if err != nil || head != c.Head() {
		t.Errorf("Latest() = %v, %v, want %v", head.Ref, err, c.Head().Ref)
	}
	want = append(want, Call{Method: "Latest"})
	fin, err := src.Finalized(ctx)
	if err != nil || fin != c.Genesis() {
		t.Errorf("Finalized() = %v, %v, want genesis", fin.Ref, err)
	}
	want = append(want, Call{Method: "Finalized"})

	for n := uint64(0); n <= 20; n++ {
		h, err := src.HeaderByNumber(ctx, n)
		wantH, _ := c.Canonical(n)
		if err != nil || h != wantH {
			t.Errorf("HeaderByNumber(%d) = %v, %v, want %v", n, h.Ref, err, wantH.Ref)
		}
		want = append(want, Call{Method: "HeaderByNumber", Number: n})
	}
	_, err = src.HeaderByNumber(ctx, 21)
	wantErr(t, err, chain.ErrNotFound, "fakechain: block 21: chain: not found")
	want = append(want, Call{Method: "HeaderByNumber", Number: 21})

	random := common.HexToHash("0x0102")
	var hashes []common.Hash
	for n := uint64(0); n <= 20; n++ {
		h, _ := c.Canonical(n)
		hashes = append(hashes, h.Ref.Hash)
	}
	for _, r := range orphans {
		hashes = append(hashes, r.Hash)
	}
	var none, some int
	for _, h := range hashes {
		got, err := src.HeaderByHash(ctx, h)
		wantH, _ := c.Header(h)
		if err != nil || got != wantH {
			t.Errorf("HeaderByHash(%s) = %v, %v, want %v", h.Hex(), got.Ref, err, wantH.Ref)
		}
		logs, err := src.Logs(ctx, h, DefaultFilter)
		b, _ := c.Block(h, DefaultFilter)
		if err != nil || !logsEqual(logs, b.Logs) {
			t.Errorf("Logs(%s) = %v, %v, want %v", h.Hex(), logs, err, b.Logs)
		}
		if len(b.Logs) == 0 {
			none++
			if logs != nil {
				t.Errorf("Logs(%s) = %v, want nil", h.Hex(), logs)
			}
		} else {
			some++
		}
		want = append(want, Call{Method: "HeaderByHash", Hash: h}, Call{Method: "Logs", Hash: h})
	}
	if none == 0 || some == 0 {
		t.Errorf("seed 2: %d blocks without and %d with matching logs; the check covers no case", none, some)
	}
	_, err = src.HeaderByHash(ctx, random)
	wantErr(t, err, chain.ErrNotFound, "fakechain: block "+random.Hex()+": chain: not found")
	_, err = src.HeaderByHash(ctx, common.Hash{})
	wantErr(t, err, chain.ErrNotFound, "fakechain: block "+common.Hash{}.Hex()+": chain: not found")
	_, err = src.Logs(ctx, random, chain.Filter{})
	wantErr(t, err, chain.ErrNotFound, "fakechain: block "+random.Hex()+": chain: not found")
	want = append(want, Call{Method: "HeaderByHash", Hash: random}, Call{Method: "HeaderByHash"},
		Call{Method: "Logs", Hash: random})

	_, err = src.LogsInRange(ctx, 5, 4, chain.Filter{})
	wantErr(t, err, chain.ErrInvalidRange, "fakechain: logs in range: from 5 > to 4: chain: invalid block range")
	_, err = src.LogsInRange(ctx, 25, 21, chain.Filter{})
	wantErr(t, err, chain.ErrInvalidRange, "fakechain: logs in range: from 25 > to 21: chain: invalid block range")
	_, err = src.LogsInRange(ctx, 1, 21, chain.Filter{})
	wantErr(t, err, chain.ErrNotFound, "fakechain: block 21: chain: not found")
	want = append(want, Call{Method: "LogsInRange", Number: 5, To: 4}, Call{Method: "LogsInRange", Number: 25, To: 21},
		Call{Method: "LogsInRange", Number: 1, To: 21})

	var concat []chain.Log
	for _, b := range c.CanonicalBlocks(DefaultFilter) {
		concat = append(concat, cloneLogs(b.Logs)...)
	}
	got, err := src.LogsInRange(ctx, 1, 20, DefaultFilter)
	if err != nil || !logsEqual(got, concat) || len(got) == 0 {
		t.Errorf("LogsInRange(1, 20) = %d logs, %v, want %d", len(got), err, len(concat))
	}
	want = append(want, Call{Method: "LogsInRange", Number: 1, To: 20})
	for n := uint64(0); n <= 20; n++ {
		h, _ := c.Canonical(n)
		b, _ := c.Block(h.Ref.Hash, DefaultFilter)
		one, err := src.LogsInRange(ctx, n, n, DefaultFilter)
		if err != nil || !logsEqual(one, b.Logs) || (b.Logs == nil) != (one == nil) {
			t.Errorf("LogsInRange(%d, %d) = %v, %v, want %v", n, n, one, err, b.Logs)
		}
		want = append(want, Call{Method: "LogsInRange", Number: n, To: n})
	}

	if calls := src.Calls(); !slices.Equal(calls, want) {
		t.Errorf("Calls() = %d calls %v,\nwant %d calls %v", len(calls), calls, len(want), want)
	}
	calls := src.Calls()
	calls[0].Method = "changed"
	if src.Calls()[0].Method != "Latest" {
		t.Error("mutating Calls() changed the record")
	}

	src.ResetCalls()
	if n := len(src.Calls()); n != 0 {
		t.Errorf("after ResetCalls, %d calls", n)
	}
	checkCancelled(t, src, hashes[1])

	// Results belong to the caller.
	first, _ := src.LogsInRange(ctx, 1, 20, DefaultFilter)
	first[0].Topics[0] = common.Hash{7}
	first[0].Data[0] = 7
	second, _ := src.LogsInRange(ctx, 1, 20, DefaultFilter)
	if !logsEqual(second, concat) {
		t.Error("mutating a LogsInRange result changed a later result")
	}
}

func TestFaults(t *testing.T) {
	ctx := t.Context()

	t.Run("stale head", func(t *testing.T) {
		c := New(3, Params{})
		c.Mine(20)
		src := NewSource(c, 3, Faults{StaleHeadPPM: 1_000_000, StaleDepth: 3})
		seen := map[uint64]int{}
		for range 100 {
			h, err := src.Latest(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := c.Canonical(h.Ref.Number)
			if h != want || h.Ref.Number < 17 || h.Ref.Number > 19 {
				t.Fatalf("Latest() = %v, want a canonical block 1 to 3 below head 20", h.Ref)
			}
			seen[20-h.Ref.Number]++
		}
		if len(seen) != 3 {
			t.Errorf("depths seen %v, want 1, 2 and 3", seen)
		}

		src.SetFaults(Faults{StaleHeadPPM: 1_000_000}) // StaleDepth 0 means 3
		for range 100 {
			h, _ := src.Latest(ctx)
			if h.Ref.Number < 17 || h.Ref.Number > 19 {
				t.Fatalf("StaleDepth 0: Latest() = %v, want 17 to 19", h.Ref)
			}
		}

		c2 := New(3, Params{})
		c2.Mine(2)
		src2 := NewSource(c2, 3, Faults{StaleHeadPPM: 1_000_000, StaleDepth: 8})
		for range 50 {
			h, _ := src2.Latest(ctx)
			if h.Ref.Number > 1 {
				t.Fatalf("head 2, depth up to 8: Latest() = %v, want 0 or 1", h.Ref)
			}
		}
	})

	t.Run("stale orphan", func(t *testing.T) {
		c := New(4, Params{})
		c.Mine(10)
		src := NewSource(c, 4, Faults{StaleOrphanPPM: 1_000_000})
		if h, _ := src.Latest(ctx); h != c.Head() {
			t.Errorf("no reorg yet: Latest() = %v, want head", h.Ref)
		}
		old := c.Head()
		src.Do(func(c *Chain) { c.Reorg(2, 3) })
		if h, _ := src.Latest(ctx); h != old {
			t.Errorf("after Reorg(2, 3): Latest() = %v, want the orphaned head %v", h.Ref, old.Ref)
		}
		src.Do(func(c *Chain) { c.SetFinalized(8) })
		if h, _ := src.Latest(ctx); h != old {
			t.Errorf("finalized at the fork point: Latest() = %v, want the orphaned head %v", h.Ref, old.Ref)
		}
		src.Do(func(c *Chain) { c.SetFinalized(9) })
		if h, _ := src.Latest(ctx); h != c.Head() {
			t.Errorf("finalized above the fork point: Latest() = %v, want head %v", h.Ref, c.Head().Ref)
		}
		// A later reorg replaces the pair; a rollback with depth 0 does not record one.
		src.Do(func(c *Chain) { c.Reorg(1, 2) })
		orphan, _ := c.Header(c.lastOrphan)
		if orphan.Ref.Number != 11 {
			t.Fatalf("last orphan = %v, want the former head 11", orphan.Ref)
		}
		if h, _ := src.Latest(ctx); h != orphan {
			t.Errorf("after Reorg(1, 2): Latest() = %v, want %v", h.Ref, orphan.Ref)
		}
		src.Do(func(c *Chain) { c.Reorg(0, 1) })
		if h, _ := src.Latest(ctx); h != orphan {
			t.Errorf("after Reorg(0, 1): Latest() = %v, want unchanged pair %v", h.Ref, orphan.Ref)
		}
		src.SetFaults(Faults{StaleOrphanPPM: 1_000_000, DropOrphans: true})
		if h, _ := src.Latest(ctx); h != orphan {
			t.Errorf("with DropOrphans: Latest() = %v, want the orphaned head %v", h.Ref, orphan.Ref)
		}
	})

	t.Run("drop orphans", func(t *testing.T) {
		c := New(5, Params{})
		c.Mine(10)
		orphan := c.Head()
		c.Reorg(2, 2)
		src := NewSource(c, 5, Faults{DropOrphans: true})
		_, err := src.HeaderByHash(ctx, orphan.Ref.Hash)
		wantErr(t, err, chain.ErrNotFound, "fakechain: block "+orphan.Ref.Hash.Hex()+": chain: not found")
		_, err = src.Logs(ctx, orphan.Ref.Hash, chain.Filter{})
		wantErr(t, err, chain.ErrNotFound, "fakechain: block "+orphan.Ref.Hash.Hex()+": chain: not found")
		head := c.Head()
		if h, err := src.HeaderByHash(ctx, head.Ref.Hash); err != nil || h != head {
			t.Errorf("HeaderByHash(head) = %v, %v", h.Ref, err)
		}
		if _, err := src.Logs(ctx, head.Ref.Hash, chain.Filter{}); err != nil {
			t.Errorf("Logs(head) error %v", err)
		}
		src.SetFaults(Faults{})
		if h, err := src.HeaderByHash(ctx, orphan.Ref.Hash); err != nil || h != orphan {
			t.Errorf("honest HeaderByHash(orphan) = %v, %v", h.Ref, err)
		}
	})

	t.Run("finalized unsupported", func(t *testing.T) {
		c := New(6, Params{})
		c.Mine(5)
		c.SetFinalized(3)
		src := NewSource(c, 6, Faults{FinalizedUnsupported: true})
		_, err := src.Finalized(ctx)
		wantErr(t, err, chain.ErrUnsupported, "fakechain: finalized: chain: unsupported")
		if calls := src.Calls(); !slices.Equal(calls, []Call{{Method: "Finalized"}}) {
			t.Errorf("Calls() = %v, want the one Finalized call", calls)
		}
		src.SetFaults(Faults{})
		if h, err := src.Finalized(ctx); err != nil || h.Ref.Number != 3 {
			t.Errorf("Finalized() = %v, %v, want block 3", h.Ref, err)
		}
	})

	t.Run("dropped logs", func(t *testing.T) {
		c := New(7, Params{})
		c.Mine(30)
		src := NewSource(c, 7, Faults{DroppedLogsPPM: 1_000_000})
		var checked int
		for _, b := range c.CanonicalBlocks(DefaultFilter) {
			logs, err := src.Logs(ctx, b.Ref.Hash, DefaultFilter)
			if err != nil || logs != nil {
				t.Errorf("block %d: Logs = %v, %v, want nil", b.Ref.Number, logs, err)
			}
			if len(b.Logs) > 0 {
				checked++
			}
		}
		if checked == 0 {
			t.Error("seed 7: no block with matching logs; the check covers no case")
		}
		if logs, _ := src.LogsInRange(ctx, 1, 30, DefaultFilter); len(logs) == 0 {
			t.Error("DroppedLogsPPM changed LogsInRange")
		}
	})

	t.Run("interleave", func(t *testing.T) {
		c, twin := New(8, Params{}), New(8, Params{})
		c.Mine(5)
		twin.Mine(5)
		src := NewSource(c, 8, Faults{InterleavePPM: 1_000_000})
		g := c.Genesis().Ref.Hash
		lookups := []func(){
			func() { _, _ = src.HeaderByHash(ctx, g) },
			func() { _, _ = src.HeaderByNumber(ctx, 0) },
			func() { _, _ = src.Logs(ctx, g, chain.Filter{}) },
			func() { _, _ = src.LogsInRange(ctx, 0, 0, chain.Filter{}) },
		}
		for i := range 40 {
			lookups[i%len(lookups)]()
			twin.Step()
			if c.Head() != twin.Head() {
				t.Fatalf("lookup %d: head %v, want %v after one Step", i, c.Head().Ref, twin.Head().Ref)
			}
		}
		_, _ = src.Latest(ctx)
		_, _ = src.Finalized(ctx)
		if c.Head() != twin.Head() {
			t.Error("Latest or Finalized ran a Step")
		}
	})

	t.Run("draw counts", func(t *testing.T) {
		c := New(9, Params{})
		c.Mine(10)
		src := NewSource(c, 9, Faults{})
		for range 10 {
			if h, _ := src.Latest(ctx); h != c.Head() {
				t.Fatalf("honest Latest() = %v, want head", h.Ref)
			}
		}
		h, _ := c.Canonical(4)
		_, _ = src.HeaderByHash(ctx, h.Ref.Hash)
		_, _ = src.HeaderByNumber(ctx, 99)
		_, _ = src.Logs(ctx, h.Ref.Hash, DefaultFilter)
		_, _ = src.Logs(ctx, common.Hash{1}, DefaultFilter)       // not found: no faults/logs draw
		_, _ = src.Logs(ctx, c.Genesis().Ref.Hash, DefaultFilter) // found, no matching log: one draw
		_, _ = src.LogsInRange(ctx, 3, 1, DefaultFilter)
		_, _ = src.Finalized(ctx)
		src.SetFaults(Faults{StaleDepth: 5}) // streams keep their positions
		_, _ = src.Latest(ctx)

		draws := []struct {
			r     *Rand
			label string
			n     int
		}{
			{src.staleRand, "faults/stale", 22},
			{src.orphanRand, "faults/orphan", 11},
			{src.interleaveRand, "faults/interleave", 6},
			{src.logsRand, "faults/logs", 2},
		}
		for _, d := range draws {
			fresh := NewRand(9, d.label)
			for range d.n {
				fresh.Uint64()
			}
			if d.r.Uint64() != fresh.Uint64() {
				t.Errorf("stream %s did not advance by exactly %d draws", d.label, d.n)
			}
		}
	})
}

// TestLatestDraws checks Latest's draws and answer against a model on the two fault streams.
func TestLatestDraws(t *testing.T) {
	ctx := t.Context()
	var orphans, stales, heads int
	for seed := uint64(1); seed <= 30; seed++ {
		c := New(seed, Params{ForkPPM: 400_000})
		src := NewSource(c, seed, Faults{StaleHeadPPM: 500_000, StaleOrphanPPM: 500_000, StaleDepth: 4})
		orphanR, staleR := NewRand(seed, "faults/orphan"), NewRand(seed, "faults/stale")
		for i := range 200 {
			src.Do(func(c *Chain) { c.Step() })
			o := orphanR.Chance(500_000)
			s := staleR.Chance(500_000)
			k := 1 + staleR.Intn(4)
			head := c.Head().Ref.Number
			want := c.Head()
			switch {
			case o && c.lastOrphan != (common.Hash{}) && c.lastFork >= c.finalized:
				want, _ = c.Header(c.lastOrphan)
				orphans++
			case s:
				want, _ = c.Canonical(head - min(k, head))
				stales++
			default:
				heads++
			}
			if got, _ := src.Latest(ctx); got != want {
				t.Fatalf("seed %d call %d: Latest() = %v, want %v", seed, i, got.Ref, want.Ref)
			}
		}
	}
	if orphans == 0 || stales == 0 || heads == 0 {
		t.Errorf("answers: %d orphan, %d stale, %d head; want each at least once", orphans, stales, heads)
	}
}

func TestConcurrency(t *testing.T) {
	ctx := t.Context()
	c := New(10, Params{})
	c.Mine(10)
	faults := Faults{StaleHeadPPM: 100_000, StaleOrphanPPM: 100_000, InterleavePPM: 10_000}
	src := NewSource(c, 10, faults)
	g := c.Genesis().Ref.Hash
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 1000 {
				switch (w + i) % 6 {
				case 0:
					_, _ = src.Latest(ctx)
				case 1:
					_, _ = src.Finalized(ctx)
				case 2:
					_, _ = src.HeaderByNumber(ctx, uint64(i%5))
				case 3:
					_, _ = src.HeaderByHash(ctx, g)
				case 4:
					_, _ = src.Logs(ctx, g, DefaultFilter)
				default:
					_, _ = src.LogsInRange(ctx, 0, 2, DefaultFilter)
				}
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			src.Do(func(c *Chain) { c.Mine(1) })
		}
	})
	wg.Go(func() {
		for range 200 {
			_ = src.Calls()
			src.SetFaults(faults)
		}
	})
	wg.Wait()
	if n := len(src.Calls()); n != 8000 {
		t.Errorf("%d calls recorded, want 8000", n)
	}
}

// wantErr fails t unless err wraps target and reads exactly msg.
func wantErr(t *testing.T, err, target error, msg string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("error %v, want one wrapping %v", err, target)
		return
	}
	if err.Error() != msg {
		t.Errorf("error %q, want %q", err.Error(), msg)
	}
}
