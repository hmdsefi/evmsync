// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

// fork returns blk(n, f) with the parent of chain base: the first block of a fork off base.
func fork(n uint64, f, base byte) chain.BlockRef {
	r := blk(n, f)
	r.Parent = h(n-1, base)
	return r
}

func block(r chain.BlockRef) Entry { return Entry{Kind: KindBlock, Block: chain.Block{Ref: r}} }

func finalBlk(r chain.BlockRef) Entry {
	return Entry{Kind: KindBlock, Final: true, Block: chain.Block{Ref: r}}
}

func revert(r chain.BlockRef) Entry   { return Entry{Kind: KindRevert, To: r} }
func finalize(r chain.BlockRef) Entry { return Entry{Kind: KindFinalize, To: r} }

// blocks returns the linked blocks from..to of chain 0.
func blocks(from, to uint64) []Entry {
	var es []Entry
	for n := from; n <= to; n++ {
		es = append(es, block(blk(n, 0)))
	}
	return es
}

func refs(rs ...chain.BlockRef) []chain.BlockRef { return rs }

// build returns NewState(cap) after es, each checked and applied.
func build(t testing.TB, cap int, es ...Entry) State {
	t.Helper()
	s := NewState(cap)
	for i, e := range es {
		if err := s.Check(e); err != nil {
			t.Fatalf("entry %d (%v): %v", i, e.Kind, err)
		}
		s.Apply(e)
	}
	return s
}

// applyOnly returns NewState(0) after es, applied without Check, as WAL replay does.
func applyOnly(es ...Entry) State {
	s := NewState(0)
	for _, e := range es {
		s.Apply(e)
	}
	return s
}

func wantMalformed(t *testing.T, err error, detail string) {
	t.Helper()
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want one wrapping ErrMalformed", err)
	}
	if got, want := err.Error(), "chainlog: malformed entry: "+detail; got != want {
		t.Errorf("error text =\n%q\nwant\n%q", got, want)
	}
}

func wantPass(t *testing.T, s *State, e Entry) {
	t.Helper()
	if err := s.Check(e); err != nil {
		t.Errorf("Check(%v) = %v, want nil", e.Kind, err)
	}
}

func wantState(t *testing.T, s State, tip, fin chain.BlockRef, win []chain.BlockRef) {
	t.Helper()
	if s.Tip() != tip {
		t.Errorf("Tip() = %v, want %v", s.Tip(), tip)
	}
	if s.Finalized() != fin {
		t.Errorf("Finalized() = %v, want %v", s.Finalized(), fin)
	}
	if !reflect.DeepEqual(s.Window(), win) {
		t.Errorf("Window() = %v, want %v", s.Window(), win)
	}
}

// wantInvariants checks that the window is strictly ascending, above F when F is set, and that
// the tip is the last window ref, or F when the window is empty.
func wantInvariants(t *testing.T, s State) {
	t.Helper()
	w := s.Window()
	for i := 1; i < len(w); i++ {
		if w[i].Number <= w[i-1].Number {
			t.Fatalf("window not ascending: %v", w)
		}
	}
	if fin := s.Finalized(); fin != (chain.BlockRef{}) && len(w) > 0 && w[0].Number <= fin.Number {
		t.Fatalf("window %v not above finalized %v", w, fin)
	}
	if len(w) > 0 && s.Tip() != w[len(w)-1] {
		t.Fatalf("tip %v is not the last window ref of %v", s.Tip(), w)
	}
	if len(w) == 0 && s.Tip() != s.Finalized() {
		t.Fatalf("empty window: tip %v, finalized %v", s.Tip(), s.Finalized())
	}
}

// snapshot is everything a State shows: its accessors and its binary form.
type snapshot struct {
	tip, fin chain.BlockRef
	win      []chain.BlockRef
	bin      []byte
}

func snap(s State) snapshot {
	b, _ := s.MarshalBinary()
	return snapshot{s.Tip(), s.Finalized(), s.Window(), b}
}

func TestNewState(t *testing.T) {
	for name, s := range map[string]State{"NewState(0)": NewState(0), "State{}": {}, "NewState(5)": NewState(5)} {
		t.Run(name, func(t *testing.T) {
			wantState(t, s, chain.BlockRef{}, chain.BlockRef{}, nil)
		})
	}

	for name, s := range map[string]State{"NewState(0)": NewState(0), "State{}": {}} {
		t.Run(name+" cap", func(t *testing.T) {
			for n := uint64(1); n <= DefaultWindowCap; n++ {
				e := block(blk(n, 0))
				if err := s.Check(e); err != nil {
					t.Fatalf("block %d: %v", n, err)
				}
				s.Apply(e)
			}
			wantMalformed(t, s.Check(block(blk(DefaultWindowCap+1, 0))), "block 100001: window is full (100000 blocks)")
		})
	}

	s := build(t, 5, blocks(1, 5)...)
	wantMalformed(t, s.Check(block(blk(6, 0))), "block 6: window is full (5 blocks)")

	if got, want := panicText(func() { NewState(-1) }), "evmsync: chainlog.NewState: cap must not be negative, got -1"; got != want {
		t.Errorf("NewState(-1) panic = %q, want %q", got, want)
	}
}

func TestRule1Increasing(t *testing.T) {
	var empty State
	wantPass(t, &empty, block(blk(0, 0)))
	wantPass(t, &empty, block(blk(7, 0)))

	s := build(t, 0, block(blk(7, 0)))
	wantMalformed(t, s.Check(block(blk(7, 0))), "block 7 is not above tip "+blk(7, 0).String())
	wantMalformed(t, s.Check(block(blk(6, 0))), "block 6 is not above tip "+blk(7, 0).String())
	wantMalformed(t, s.Check(finalBlk(blk(6, 0))), "block 6 is not above tip "+blk(7, 0).String())
	wantPass(t, &s, block(blk(8, 0)))
	wantMalformed(t, s.Check(block(blk(9, 0))), "block 9: non-final block is not the child of tip "+blk(7, 0).String())

	s = build(t, 0, block(blk(7, 0)), block(blk(8, 0)), block(blk(9, 0)), revert(blk(7, 0)))
	wantPass(t, &s, block(blk(8, 0)))
	wantPass(t, &s, block(fork(8, 1, 0)))
	wantMalformed(t, s.Check(block(blk(7, 1))), "block 7 is not above tip "+blk(7, 0).String())

	// After a Finalize above the tip, the next block is above its number.
	s = build(t, 0, block(blk(7, 0)), finalize(blk(12, 0)))
	wantMalformed(t, s.Check(block(blk(12, 0))), "block 12 is not above tip "+blk(12, 0).String())
	wantPass(t, &s, block(blk(13, 0)))
}

func TestRule2Linked(t *testing.T) {
	s := build(t, 0, block(blk(7, 0)))
	bad := blk(8, 0)
	bad.Parent = blk(7, 1).Hash
	wantMalformed(t, s.Check(block(bad)), "block 8: parent "+blk(7, 1).Hash.Hex()+" is not tip hash "+blk(7, 0).Hash.Hex())
	wantPass(t, &s, block(blk(8, 0)))
	wantMalformed(t, s.Check(block(blk(12, 0))), "block 12: non-final block is not the child of tip "+blk(7, 0).String())

	s = build(t, 0, finalize(blk(7, 0)))
	free := blk(12, 0)
	free.Parent = h(99, 5)
	wantPass(t, &s, finalBlk(free))
	// A Final block right above the tip still links to it.
	wantMalformed(t, s.Check(finalBlk(bad)), "block 8: parent "+blk(7, 1).Hash.Hex()+" is not tip hash "+blk(7, 0).Hash.Hex())
	wantPass(t, &s, finalBlk(blk(8, 0)))
}

func TestRule3RevertTarget(t *testing.T) {
	var empty State
	wantMalformed(t, empty.Check(revert(blk(5, 0))), "revert to "+blk(5, 0).String()+": target is not below tip "+chain.BlockRef{}.String())

	s := build(t, 0, append([]Entry{finalize(blk(5, 0))}, blocks(6, 8)...)...)
	tip := blk(8, 0).String()
	wantMalformed(t, s.Check(revert(blk(8, 0))), "revert to "+blk(8, 0).String()+": target is not below tip "+tip)
	wantMalformed(t, s.Check(revert(blk(9, 0))), "revert to "+blk(9, 0).String()+": target is not below tip "+tip)
	wantMalformed(t, s.Check(revert(blk(7, 1))), "revert to "+blk(7, 1).String()+": target is neither the finalized block nor in the window")
	wantPass(t, &s, revert(blk(7, 0)))
	wantPass(t, &s, revert(blk(6, 0)))
	wantPass(t, &s, revert(blk(5, 0)))
	late := blk(6, 0)
	late.Time++
	wantMalformed(t, s.Check(revert(late)), "revert to "+late.String()+": target is neither the finalized block nor in the window")
	otherParent := blk(5, 0)
	otherParent.Parent = h(4, 1)
	wantMalformed(t, s.Check(revert(otherParent)), "revert to "+otherParent.String()+": target is neither the finalized block nor in the window")
	wantMalformed(t, s.Check(revert(blk(4, 0))), "revert to "+blk(4, 0).String()+": target is neither the finalized block nor in the window")

	s.Apply(revert(blk(6, 0)))
	wantState(t, s, blk(6, 0), blk(5, 0), refs(blk(6, 0)))
	s.Apply(revert(blk(5, 0)))
	wantState(t, s, blk(5, 0), blk(5, 0), nil)
}

func TestRule4Finality(t *testing.T) {
	base := append([]Entry{finalize(blk(5, 0))}, blocks(6, 9)...)
	s := build(t, 0, base...)
	wantMalformed(t, s.Check(finalize(blk(4, 0))), "finalize to "+blk(4, 0).String()+": below finalized "+blk(5, 0).String())
	wantMalformed(t, s.Check(finalize(blk(5, 1))), "finalize to "+blk(5, 1).String()+": conflicts with finalized "+blk(5, 0).String())
	wantMalformed(t, s.Check(finalize(blk(7, 1))), "finalize to "+blk(7, 1).String()+": conflicts with window block "+blk(7, 0).String())
	late := blk(7, 0)
	late.Time++
	wantMalformed(t, s.Check(finalize(late)), "finalize to "+late.String()+": conflicts with window block "+blk(7, 0).String())
	tipChild := fork(10, 0, 1)
	wantMalformed(t, s.Check(finalize(tipChild)), "finalize to "+tipChild.String()+": parent "+h(9, 1).Hex()+" is not tip hash "+h(9, 0).Hex())

	// A repeated Finalize passes and changes nothing.
	wantPass(t, &s, finalize(blk(5, 0)))
	before := snap(s)
	s.Apply(finalize(blk(5, 0)))
	if after := snap(s); !reflect.DeepEqual(after, before) {
		t.Errorf("repeated Finalize changed the state: %+v, want %+v", after, before)
	}

	// A Finalize inside the window prunes it.
	wantPass(t, &s, finalize(blk(7, 0)))
	s.Apply(finalize(blk(7, 0)))
	wantState(t, s, blk(9, 0), blk(7, 0), refs(blk(8, 0), blk(9, 0)))

	// A Finalize above the tip moves the tip and empties the window.
	s = build(t, 0, base...)
	wantPass(t, &s, finalize(blk(12, 0)))
	s.Apply(finalize(blk(12, 0)))
	wantState(t, s, blk(12, 0), blk(12, 0), nil)

	// Check 4 on a sparse window, which only Apply can build.
	sparse := applyOnly(finalize(blk(10, 0)), block(fork(20, 0, 3)), block(fork(30, 0, 3)))
	wantMalformed(t, sparse.Check(finalize(blk(29, 1))), "finalize to "+blk(29, 1).String()+": window block "+fork(30, 0, 3).String()+" does not link to it")
	linked := blk(29, 3)
	wantPass(t, &sparse, finalize(linked))

	// A sparse window has a ref at index n - first that is not numbered n.
	wantPass(t, &sparse, finalize(blk(21, 3)))

	var empty State
	wantPass(t, &empty, finalize(blk(3, 0)))
	empty.Apply(finalize(blk(3, 0)))
	wantState(t, empty, blk(3, 0), blk(3, 0), nil)

	// Genesis: a Finalize of block 0 sets the tip, so later blocks link to it.
	var genesis State
	wantPass(t, &genesis, finalize(blk(0, 0)))
	genesis.Apply(finalize(blk(0, 0)))
	wantState(t, genesis, blk(0, 0), blk(0, 0), nil)
	wantMalformed(t, genesis.Check(block(blk(5, 0))), "block 5: non-final block is not the child of tip "+blk(0, 0).String())

	// Numbers at the ends of uint64.
	maxRef := chain.BlockRef{Number: math.MaxUint64, Hash: h(math.MaxUint64, 0), Parent: h(9, 9)}
	low := build(t, 0, block(blk(0, 0)))
	wantPass(t, &low, finalize(maxRef))
	low.Apply(finalize(maxRef))
	wantState(t, low, maxRef, maxRef, nil)
	high := build(t, 0, block(maxRef))
	wantPass(t, &high, finalize(blk(0, 0)))
	high.Apply(finalize(blk(0, 0)))
	wantState(t, high, maxRef, blk(0, 0), refs(maxRef))
}

func TestRule5FinalBlocks(t *testing.T) {
	s := build(t, 0, finalize(blk(5, 0)), block(blk(6, 0)))
	wantMalformed(t, s.Check(finalBlk(blk(8, 0))), "block 8: final block with 1 unfinalized blocks in the window")

	s = build(t, 0, finalize(blk(5, 0)), finalBlk(blk(8, 0)))
	wantState(t, s, blk(8, 0), blk(8, 0), nil)
	for _, e := range blocks(9, 10) {
		wantPass(t, &s, e)
		s.Apply(e)
	}
	wantMalformed(t, s.Check(finalBlk(blk(12, 0))), "block 12: final block with 2 unfinalized blocks in the window")
	wantPass(t, &s, revert(blk(8, 0)))
	wantMalformed(t, s.Check(revert(blk(5, 0))), "revert to "+blk(5, 0).String()+": target is neither the finalized block nor in the window")

	var empty State
	wantPass(t, &empty, finalBlk(blk(3, 0)))
	empty.Apply(finalBlk(blk(3, 0)))
	wantState(t, empty, blk(3, 0), blk(3, 0), nil)

	// Apply empties the window for a Final block even without Check, as WAL replay applies.
	s = applyOnly(block(blk(1, 0)), block(blk(2, 0)), finalBlk(blk(5, 0)))
	wantState(t, s, blk(5, 0), blk(5, 0), nil)
}

func TestRule6Window(t *testing.T) {
	s := build(t, 3, blocks(1, 3)...)
	wantMalformed(t, s.Check(block(blk(4, 0))), "block 4: window is full (3 blocks)")
	wantPass(t, &s, finalize(blk(2, 0)))
	wantPass(t, &s, revert(blk(2, 0)))
	s.Apply(finalize(blk(2, 0)))
	wantPass(t, &s, block(blk(4, 0)))
}

// lg returns a log of block r with the given index and tx index.
func lg(r chain.BlockRef, index, txIndex uint32) chain.Log {
	return chain.Log{BlockNumber: r.Number, BlockHash: r.Hash, Index: index, TxIndex: txIndex}
}

func withLogs(r chain.BlockRef, logs ...chain.Log) Entry {
	return Entry{Kind: KindBlock, Block: chain.Block{Ref: r, Logs: logs}}
}

func TestShape(t *testing.T) {
	r := blk(7, 0)
	wrongNumber := lg(r, 0, 0)
	wrongNumber.BlockNumber = 8
	wrongHash := lg(r, 0, 0)
	wrongHash.BlockHash = blk(8, 0).Hash
	five := lg(r, 0, 0)
	five.Topics = make([]common.Hash, 5)
	blockWithTo := block(r)
	blockWithTo.To = blk(3, 0)
	revertFinal := revert(blk(3, 0))
	revertFinal.Final = true
	finalizeLogs := finalize(blk(3, 0))
	finalizeLogs.Block.Logs = []chain.Log{lg(r, 0, 0)}
	revertRef := revert(blk(3, 0))
	revertRef.Block.Ref = r
	kind0 := Entry{Block: chain.Block{Ref: chain.BlockRef{Number: 7}}}
	kindAndTo := Entry{Kind: 9, To: blk(3, 0)}
	toAndZeroHash := block(chain.BlockRef{Number: 7})
	toAndZeroHash.To = blk(3, 0)
	revertFinalZero := revert(chain.BlockRef{Number: 3})
	revertFinalZero.Final = true
	wrongBoth := lg(r, 0, 0)
	wrongBoth.BlockNumber, wrongBoth.BlockHash = 8, blk(8, 0).Hash

	tests := []struct {
		name   string
		e      Entry
		detail string
	}{
		{"kind 0", Entry{}, "unknown kind 0"},
		{"kind 4", Entry{Kind: 4, To: blk(3, 0)}, "unknown kind 4"},
		{"kind row wins", kind0, "unknown kind 0"},
		{"kind row wins over To", kindAndTo, "unknown kind 9"},
		{"block with To", blockWithTo, "block 7: To must be zero"},
		{"To row wins over zero hash", toAndZeroHash, "block 7: To must be zero"},
		{"revert with Final", revertFinal, "revert entry: Block and Final must be zero"},
		{"revert with a block ref", revertRef, "revert entry: Block and Final must be zero"},
		{"finalize with logs", finalizeLogs, "finalize entry: Block and Final must be zero"},
		{"block with zero hash", block(chain.BlockRef{Number: 7, Parent: h(6, 0)}), "block 7: zero hash"},
		{"revert to zero hash", revert(chain.BlockRef{Number: 4}), "revert to 4: zero hash"},
		{"finalize to zero hash", finalize(chain.BlockRef{Number: 4, Time: 9}), "finalize to 4: zero hash"},
		{"log block number", withLogs(r, wrongNumber), "block 7: log 0: block number 8"},
		{"log block hash", withLogs(r, lg(r, 0, 0), wrongHash), "block 7: log 1: block hash " + blk(8, 0).Hash.Hex()},
		{"5 topics", withLogs(r, five), "block 7: log 0: 5 topics, at most 4"},
		{"index 3 then 3", withLogs(r, lg(r, 3, 0), lg(r, 3, 0)), "block 7: log 1: index 3 not above 3"},
		{"index 3 then 2", withLogs(r, lg(r, 3, 0), lg(r, 2, 0)), "block 7: log 1: index 2 not above 3"},
		{"tx index 2 then 1", withLogs(r, lg(r, 0, 2), lg(r, 1, 1)), "block 7: log 1: tx index 1 below 2"},
		// Entries that fail two adjacent rows report the first.
		{"Final row wins over zero hash", revertFinalZero, "revert entry: Block and Final must be zero"},
		{"block number row wins over hash", withLogs(r, wrongBoth), "block 7: log 0: block number 8"},
		{"index row wins over tx index", withLogs(r, lg(r, 3, 2), lg(r, 3, 1)), "block 7: log 1: index 3 not above 3"},
	}
	var empty State
	// A state whose rules every one of these entries would also fail: shape is checked first.
	s := build(t, 0, blocks(7, 9)...)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantMalformed(t, empty.Check(tt.e), tt.detail)
			wantMalformed(t, s.Check(tt.e), tt.detail)
		})
	}

	ok := withLogs(r, lg(r, 0, 0), lg(r, 1, 0), lg(r, 5, 3))
	ok.Block.Logs[2].Topics = make([]common.Hash, 4)
	wantPass(t, &empty, ok)
	emptyLogs := revert(blk(3, 0))
	emptyLogs.Block.Logs = []chain.Log{}
	wantMalformed(t, empty.Check(emptyLogs), "revert to "+blk(3, 0).String()+": target is not below tip "+chain.BlockRef{}.String())
}

func TestSize(t *testing.T) {
	r := blk(7, 0)
	sized := func(dataLen int) Entry {
		l := lg(r, 0, 0)
		l.Data = make([]byte, dataLen)
		return withLogs(r, l)
	}
	bodySize := func(e Entry) int {
		e.Offset = math.MaxUint64
		return len(Encode(nil, e)) - 8
	}
	var empty State
	wantPass(t, &empty, sized(MaxRecordSize-200))
	tooBig := sized(MaxRecordSize)
	wantMalformed(t, empty.Check(tooBig), "block 7: record body of "+strconv.Itoa(bodySize(tooBig))+" bytes exceeds 16777216")

	// The largest data that fits gives a body of exactly MaxRecordSize.
	fixed := bodySize(sized(0)) - 1 // without the one-byte data length
	exact := sized(MaxRecordSize - fixed - 4)
	if got := bodySize(exact); got != MaxRecordSize {
		t.Fatalf("test setup: body of %d bytes, want %d", got, MaxRecordSize)
	}
	wantPass(t, &empty, exact)
	over := sized(MaxRecordSize - fixed - 3)
	wantMalformed(t, empty.Check(over), "block 7: record body of 16777217 bytes exceeds 16777216")

	if a := testing.AllocsPerRun(10, func() { _ = empty.Check(exact) }); a != 0 {
		t.Errorf("Check of a 16 MiB block: %v allocs, want 0", a)
	}
	s := build(t, 0, blocks(1, 1000)...)
	next := block(blk(1001, 0))
	if a := testing.AllocsPerRun(100, func() { _ = s.Check(next) }); a != 0 {
		t.Errorf("Check of a block: %v allocs, want 0", a)
	}
}

// step is one entry of the apply sequence and the state after it.
type step struct {
	e        Entry
	tip, fin chain.BlockRef
	win      []chain.BlockRef
}

// applySteps is a sequence through every kind: blocks, a revert, a fork, a finalize inside the
// window.
func applySteps() []step {
	f4, f5, f6, f7 := fork(4, 1, 0), blk(5, 1), blk(6, 1), blk(7, 1)
	b := func(n uint64) chain.BlockRef { return blk(n, 0) }
	zero := chain.BlockRef{}
	return []step{
		{block(b(1)), b(1), zero, refs(b(1))},
		{block(b(2)), b(2), zero, refs(b(1), b(2))},
		{block(b(3)), b(3), zero, refs(b(1), b(2), b(3))},
		{block(b(4)), b(4), zero, refs(b(1), b(2), b(3), b(4))},
		{block(b(5)), b(5), zero, refs(b(1), b(2), b(3), b(4), b(5))},
		{revert(b(3)), b(3), zero, refs(b(1), b(2), b(3))},
		{block(f4), f4, zero, refs(b(1), b(2), b(3), f4)},
		{block(f5), f5, zero, refs(b(1), b(2), b(3), f4, f5)},
		{block(f6), f6, zero, refs(b(1), b(2), b(3), f4, f5, f6)},
		{finalize(f4), f6, f4, refs(f5, f6)},
		{block(f7), f7, f4, refs(f5, f6, f7)},
	}
}

func TestApplySequence(t *testing.T) {
	var s State
	for i, st := range applySteps() {
		if err := s.Check(st.e); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		s.Apply(st.e)
		wantState(t, s, st.tip, st.fin, st.win)
		wantInvariants(t, s)
	}

	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // a seeded, reproducible sequence, not a secret
	var fuzz State
	for range 1000 {
		n := r.Uint64N(40)
		forks := [...]byte{0, 1, 2}
		kinds := [...]Kind{0, KindBlock, KindRevert, KindFinalize, 4}
		ref := chain.BlockRef{Number: n, Hash: h(n, forks[r.IntN(3)]), Parent: h(n-1, forks[r.IntN(3)]), Time: r.Uint64()}
		e := Entry{Kind: kinds[r.IntN(len(kinds))], Final: r.IntN(4) == 0}
		if e.Kind == KindBlock {
			e.Block.Ref = ref
		} else {
			e.To = ref
		}
		_ = fuzz.Check(e)
		fuzz.Apply(e)
		_ = fuzz.Window()
		_, _ = fuzz.MarshalBinary()
		_ = fuzz.Clone()
	}
}

func TestIsolation(t *testing.T) {
	five := refs(blk(1, 0), blk(2, 0), blk(3, 0), blk(4, 0), blk(5, 0))
	s := build(t, 0, blocks(1, 5)...)
	w := s.Window()
	w[0] = chain.BlockRef{}
	if !reflect.DeepEqual(s.Window(), five) {
		t.Errorf("changing Window()'s result changed the state: %v", s.Window())
	}

	c := s.Clone()
	c.Apply(revert(blk(3, 0)))
	c.Apply(block(fork(4, 1, 0)))
	c.Apply(block(blk(5, 1)))
	if !reflect.DeepEqual(s.Window(), five) {
		t.Errorf("applying to a clone changed the original: %v", s.Window())
	}

	c = s.Clone()
	s.Apply(revert(blk(3, 0)))
	s.Apply(block(fork(4, 1, 0)))
	s.Apply(block(blk(5, 1)))
	if !reflect.DeepEqual(c.Window(), five) {
		t.Errorf("applying to the original changed a clone: %v", c.Window())
	}
	wantState(t, c, blk(5, 0), chain.BlockRef{}, five)

	// Clone keeps the cap.
	small := build(t, 2, blocks(1, 2)...)
	sc := small.Clone()
	wantMalformed(t, sc.Check(block(blk(3, 0))), "block 3: window is full (2 blocks)")

	var empty State
	if w := empty.Clone().Window(); w != nil {
		t.Errorf("clone of an empty state: Window() = %v, want nil", w)
	}
}

func TestCheckBatch(t *testing.T) {
	stateA := func() State { return build(t, 0, append([]Entry{finalize(blk(0, 0))}, blocks(1, 5)...)...) }
	stateB := build(t, 0, append([]Entry{finalize(blk(0, 0))}, blocks(1, 4)...)...)
	f6 := finalBlk(blk(6, 0))
	// Longer than CheckBatch's 8-entry stack buffer: a revert, 15 blocks of a fork, a second
	// revert, a block and a finalize.
	longBatch := []Entry{revert(blk(3, 0)), block(fork(4, 1, 0))}
	for n := uint64(5); n <= 18; n++ {
		longBatch = append(longBatch, block(blk(n, 1)))
	}
	longBatch = append(longBatch, revert(blk(10, 1)), block(fork(11, 2, 1)), finalize(blk(5, 1)))

	tests := []struct {
		name   string
		s      State
		batch  []Entry
		index  int
		detail string
	}{
		{"a", stateA(), []Entry{revert(blk(3, 0)), block(fork(4, 1, 0)), block(blk(4, 1))}, 2, "block 4 is not above tip " + fork(4, 1, 0).String()},
		{"b", stateB, []Entry{block(blk(5, 0)), f6}, 1, "block 6: final block with 5 unfinalized blocks in the window"},
		{"c", stateA(), []Entry{revert(blk(3, 0)), block(fork(4, 1, 0))}, -1, ""},
		{"first fails", stateA(), []Entry{block(blk(9, 0)), block(blk(6, 0))}, 0, "block 9: non-final block is not the child of tip " + blk(5, 0).String()},
		{"finalize, revert, blocks, fail", stateA(), []Entry{
			finalize(blk(2, 0)), revert(blk(3, 0)), block(fork(4, 1, 0)), block(blk(5, 1)), block(blk(9, 9)),
		}, 4, "block 9: non-final block is not the child of tip " + blk(5, 1).String()},
		{"revert to F, final blocks, fail", stateA(), []Entry{
			revert(blk(0, 0)), finalBlk(blk(2, 2)), block(blk(3, 2)), block(blk(4, 2)), revert(blk(1, 0)),
		}, 4, "revert to " + blk(1, 0).String() + ": target is neither the finalized block nor in the window"},
		{"empty batch", stateA(), nil, -1, ""},
		{"19 entries", stateA(), longBatch, -1, ""},
		{"19 entries, then a failure", stateA(), append(slices.Clone(longBatch), block(blk(30, 0))), 19, "block 30: non-final block is not the child of tip " + fork(11, 2, 1).String()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.s
			before := snap(s)
			i, err := s.CheckBatch(tt.batch)
			if i != tt.index {
				t.Errorf("index = %d, want %d", i, tt.index)
			}
			if tt.index < 0 {
				if err != nil {
					t.Errorf("error = %v, want nil", err)
				}
			} else {
				wantMalformed(t, err, tt.detail)
			}
			if after := snap(s); !reflect.DeepEqual(after, before) {
				t.Errorf("CheckBatch changed the state:\n%+v\nwant\n%+v", after, before)
			}
		})
	}

	// The cost does not grow with the window: no clone, no allocation for a block or a finalize.
	s := build(t, 0, blocks(1, 50_000)...)
	batch := []Entry{block(blk(50_001, 0)), finalize(blk(10, 0)), block(blk(50_002, 0))}
	if a := testing.AllocsPerRun(100, func() { _, _ = s.CheckBatch(batch) }); a != 0 {
		t.Errorf("CheckBatch on a window of 50,000: %v allocs, want 0", a)
	}
}

func TestStateSparseBackfillPrefix(t *testing.T) {
	batch := []Entry{finalBlk(blk(3, 0)), finalBlk(blk(4, 0)), finalBlk(blk(9, 0)), finalize(blk(11, 0))}
	tips := []chain.BlockRef{blk(1, 0), blk(3, 0), blk(4, 0), blk(9, 0), blk(11, 0)}
	for k := range len(batch) + 1 {
		checked := build(t, 0, append([]Entry{finalize(blk(1, 0))}, batch[:k]...)...)
		replayed := applyOnly(append([]Entry{finalize(blk(1, 0))}, batch[:k]...)...)
		for name, s := range map[string]State{"checked": checked, "applied": replayed} {
			t.Run(name+"/"+strconv.Itoa(k), func(t *testing.T) {
				wantState(t, s, tips[k], tips[k], nil)
				wantInvariants(t, s)
				if i, err := s.CheckBatch(batch[k:]); i != -1 || err != nil {
					t.Errorf("CheckBatch(rest) = %d, %v; want -1, nil", i, err)
				}
				wantPass(t, &s, block(blk(tips[k].Number+1, 0)))

				b, _ := s.MarshalBinary()
				var u State
				if err := u.UnmarshalBinary(b); err != nil {
					t.Fatalf("UnmarshalBinary: %v", err)
				}
				wantState(t, u, tips[k], tips[k], nil)
			})
		}
	}
}
