// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker

import (
	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// DefaultWindowCap is the window cap used when Config.WindowCap is 0.
const DefaultWindowCap = 100_000

// Config configures a Tracker.
type Config struct {
	// Filter is passed unchanged to Source.Logs for every new block.
	Filter chain.Filter
	// WindowCap is the most unfinalized blocks the window may hold. 0 means
	// DefaultWindowCap. A negative value makes New panic.
	WindowCap int
}

// Tracker turns new heads and finalized blocks into canonical-log entries.
//
// A Tracker is not safe for concurrent use. It starts no goroutines, never sleeps
// and never reads the clock: given the same Source answers it makes the same Source
// calls and returns the same entries.
type Tracker struct {
	src    chain.Source
	filter chain.Filter
	cap    int

	// fin is the finalized ref F; its zero hash means the tracker is unanchored.
	fin chain.BlockRef
	// win[start:] is the window W: the refs numbered fin.Number+1, fin.Number+2, … in order.
	// Finalize advances start instead of copying.
	win   []chain.BlockRef
	start int
	// byHash maps the hash of each window ref to its number. It is only looked up, never
	// ranged over.
	byHash map[common.Hash]uint64
}

// New returns a Tracker whose view of the chain is st: the finalized ref, the tip
// and the window of a log's validator state (chainlog.Log.State). For an empty log,
// pass chainlog.NewState(cap); the Tracker is then unanchored until Finalize.
//
// New panics if src is nil, if cfg.WindowCap is negative, if st has blocks but no
// finalized ref, or if st's window is not a parent-linked chain above its finalized
// ref.
func New(src chain.Source, cfg Config, st chainlog.State) *Tracker {
	if src == nil {
		panic("evmsync: tracker.New: nil Source")
	}
	if cfg.WindowCap < 0 {
		panic("evmsync: tracker.New: negative WindowCap")
	}
	fin, tip, win := st.Finalized(), st.Tip(), st.Window()
	if fin.Hash == (common.Hash{}) && tip != (chain.BlockRef{}) {
		panic("evmsync: tracker.New: state has blocks but no finalized block")
	}
	if !linked(fin, tip, win) {
		panic("evmsync: tracker.New: window is not a parent-linked chain above the finalized block")
	}
	t := &Tracker{
		src:    src,
		filter: cfg.Filter,
		cap:    cfg.WindowCap,
		fin:    fin,
		win:    win,
		byHash: make(map[common.Hash]uint64, len(win)),
	}
	if t.cap == 0 {
		t.cap = DefaultWindowCap
	}
	for _, r := range win {
		t.byHash[r.Hash] = r.Number
	}
	return t
}

// linked reports whether win is the contiguous, parent-linked chain above fin that ends at tip,
// or empty with tip equal to fin.
func linked(fin, tip chain.BlockRef, win []chain.BlockRef) bool {
	prev := fin
	for _, r := range win {
		if r.Parent != prev.Hash || r.Number != prev.Number+1 {
			return false
		}
		prev = r
	}
	return prev == tip
}

// State returns a validator state whose Tip, Finalized and Window equal the Tracker's
// view. Later calls on the Tracker never change a State already returned.
func (t *Tracker) State() chainlog.State {
	s := chainlog.NewState(t.cap)
	if !t.anchored() {
		return s
	}
	s.Apply(chainlog.Entry{Kind: chainlog.KindFinalize, To: t.fin})
	for _, r := range t.window() {
		s.Apply(chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r}})
	}
	return s
}

func (t *Tracker) anchored() bool { return t.fin.Hash != (common.Hash{}) }

// window returns the live window. The caller must not keep it across a change.
func (t *Tracker) window() []chain.BlockRef { return t.win[t.start:] }

// tip returns T: the last window ref, or F when the window is empty.
func (t *Tracker) tip() chain.BlockRef {
	if w := t.window(); len(w) > 0 {
		return w[len(w)-1]
	}
	return t.fin
}

// known returns the ref of K (F or a window ref) with hash h.
func (t *Tracker) known(h common.Hash) (chain.BlockRef, bool) {
	if h == t.fin.Hash && t.anchored() {
		return t.fin, true
	}
	return t.inWindow(h)
}

// inWindow returns the window ref with hash h.
func (t *Tracker) inWindow(h common.Hash) (chain.BlockRef, bool) {
	n, ok := t.byHash[h]
	if !ok {
		return chain.BlockRef{}, false
	}
	return t.window()[n-t.fin.Number-1], true
}

// cut drops the window refs above number n.
func (t *Tracker) cut(n uint64) {
	w := t.window()
	keep := n - t.fin.Number
	for _, r := range w[keep:] {
		delete(t.byHash, r.Hash)
	}
	t.win = t.win[:t.start+int(keep)] //nolint:gosec // keep <= len(w)
}

// push appends r to the window.
func (t *Tracker) push(r chain.BlockRef) {
	t.win = append(t.win, r)
	t.byHash[r.Hash] = r.Number
}

// finalizeTo makes f, a window ref, the finalized ref and drops the window refs at or below it.
func (t *Tracker) finalizeTo(f chain.BlockRef) {
	drop := int(f.Number - t.fin.Number) //nolint:gosec // f is a window ref, so drop <= len(window)
	for _, r := range t.window()[:drop] {
		delete(t.byHash, r.Hash)
	}
	t.start += drop
	t.fin = f
	// Compact once the dead prefix is longer than the window, so pruning stays amortized O(1).
	if t.start > len(t.win)-t.start {
		n := copy(t.win, t.win[t.start:])
		t.win = t.win[:n]
		t.start = 0
	}
}

// reset makes f the finalized ref and the tip, with an empty window.
func (t *Tracker) reset(f chain.BlockRef) {
	clear(t.byHash)
	t.win = t.win[:0]
	t.start = 0
	t.fin = f
}
