// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"encoding/binary"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// TestMemoryPendingBounded runs 10,000 blocks with finality 5 blocks behind the tip and a
// depth-2 revert every 100 blocks. The pending array stays small and every slot outside the live
// blocks is zero, so retired blocks and their logs can be collected. A Finalize of every pending
// block drops the snapshot.
func TestMemoryPendingBounded(t *testing.T) {
	m := NewMemory([]uint64{},
		func(s []uint64, b chain.Block) []uint64 { return append(s, b.Ref.Number) },
		slices.Clone[[]uint64])
	ref := func(n uint64) chain.BlockRef {
		var h common.Hash
		binary.BigEndian.PutUint64(h[24:], n+1)
		return chain.BlockRef{Number: n, Hash: h}
	}
	block := func(n uint64) chainlog.Entry {
		r := ref(n)
		logs := []chain.Log{{BlockNumber: n, BlockHash: r.Hash, Data: []byte{1}}}
		return chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r, Logs: logs}}
	}
	apply := func(e chainlog.Entry) {
		t.Helper()
		e.Offset = m.off + 1
		if err := m.Apply(t.Context(), e); err != nil {
			t.Fatalf("Apply offset %d (%s): %v", e.Offset, e.Kind, err)
		}
	}
	checkSlots := func(n uint64) {
		t.Helper()
		if c := cap(m.pending); c > 32 {
			t.Fatalf("block %d: pending capacity %d, want at most 32", n, c)
		}
		full := m.pending[:cap(m.pending)]
		for i := range full {
			if i >= m.first && i < len(m.pending) {
				continue
			}
			if full[i].Ref != (chain.BlockRef{}) || full[i].Logs != nil {
				t.Fatalf("block %d: slot %d outside the live blocks [%d, %d) holds block %d", n, i, m.first, len(m.pending), full[i].Ref.Number)
			}
		}
	}

	apply(chainlog.Entry{Kind: chainlog.KindFinalize, To: ref(0)})
	for n := uint64(1); n <= 10_000; n++ {
		apply(block(n))
		if n%100 == 0 {
			apply(chainlog.Entry{Kind: chainlog.KindRevert, To: ref(n - 2)})
			checkSlots(n)
			apply(block(n - 1))
			apply(block(n))
		}
		if n > 5 {
			apply(chainlog.Entry{Kind: chainlog.KindFinalize, To: ref(n - 5)})
		}
		checkSlots(n)
	}
	want := make([]uint64, 10_000)
	for i := range want {
		want[i] = uint64(i) + 1
	}
	if got := m.State(); !slices.Equal(got, want) {
		t.Fatalf("State() has %d numbers, want 1 to 10000", len(got))
	}

	apply(chainlog.Entry{Kind: chainlog.KindFinalize, To: ref(10_000)})
	checkSlots(10_000)
	if len(m.pending) != 0 || m.snap != nil {
		t.Errorf("after finalizing every block: %d pending, snapshot %v; want none and nil", len(m.pending), m.snap)
	}
}
