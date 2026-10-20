// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

// State validates entries against the rules of the log and tracks the tip, the finalized ref F
// and the window: the refs of the non-reverted, non-final blocks above F, ascending by number.
// The zero State is an empty state with cap DefaultWindowCap. A State is a value: copies made
// with Clone are independent; plain assignment shares the window and must not be used after
// either copy changes. A State is not safe for concurrent use.
//
// Check accepts an entry when its fields fit its kind and:
//   - a block is above the tip, and a block one above the tip has the tip's hash as parent. A
//     non-final block is always one above the tip. A Final block needs an empty window, may be
//     further above the tip, and becomes the tip and F;
//   - a revert is below the tip and targets F or a window ref;
//   - a finalize does not go below F, does not conflict with F or a window ref at its number, and
//     links to the window ref above it and to the tip when it is the tip's child;
//   - a non-final block does not grow the window beyond the cap;
//   - every log of a block carries the block's number and hash, at most 4 topics, increasing
//     indexes and non-decreasing transaction indexes, and the block's record fits MaxRecordSize.
type State struct {
	cap int              // the window cap; 0 means DefaultWindowCap
	tip chain.BlockRef   // the newest non-reverted block ref
	fin chain.BlockRef   // F, the finalized ref
	win []chain.BlockRef // the window; Finalize re-slices its front, Block appends at its back
}

// NewState returns an empty State whose window holds at most cap refs. cap 0 means
// DefaultWindowCap. It panics if cap < 0.
func NewState(cap int) State {
	if cap < 0 {
		panic("evmsync: chainlog.NewState: cap must not be negative, got " + strconv.Itoa(cap))
	}
	return State{cap: cap}
}

// Tip returns the newest non-reverted block ref, or the zero ref.
func (s State) Tip() chain.BlockRef { return s.tip }

// Finalized returns F, or the zero ref.
func (s State) Finalized() chain.BlockRef { return s.fin }

// Window returns a new slice of the window's refs in ascending number order, nil when empty.
func (s State) Window() []chain.BlockRef {
	if len(s.win) == 0 {
		return nil
	}
	return slices.Clone(s.win)
}

// Clone returns an independent copy of s.
func (s State) Clone() State {
	s.win = s.Window()
	return s
}

// Check returns nil if e may follow the entries already applied, and otherwise an error wrapping
// ErrMalformed. It does not change s.
func (s *State) Check(e Entry) error {
	if err := checkShape(&e); err != nil {
		return err
	}
	switch e.Kind {
	case KindBlock:
		if n := recordBodySize(e); n > MaxRecordSize {
			return malformed("block %d: record body of %d bytes exceeds %d", e.Block.Ref.Number, n, MaxRecordSize)
		}
		return s.checkBlock(&e)
	case KindRevert:
		return s.checkRevert(e.To)
	default:
		return s.checkFinalize(e.To)
	}
}

// CheckBatch checks entries in order, each against the state after the ones before it. It
// returns -1 and nil when all pass, and otherwise the index of the first failing entry and its
// Check error. s is unchanged when CheckBatch returns; it changes s while it runs, so callers
// hold the lock that guards s.
//
// Its cost grows with len(entries) and with the refs a Revert in the batch drops, never with the
// window's length.
func (s *State) CheckBatch(entries []Entry) (int, error) {
	// Room for every block of the batch, made once: an append that grows the window inside the
	// loop would copy all of it, and the undo would throw the copy away.
	blocks := 0
	for i := range entries {
		if entries[i].Kind == KindBlock && !entries[i].Final {
			blocks++
		}
	}
	if cap(s.win)-len(s.win) < blocks {
		s.win = slices.Grow(s.win, blocks)
	}
	var buf [8]undo
	undos := buf[:0]
	for i := range entries {
		e := &entries[i]
		if err := s.Check(*e); err != nil {
			s.undo(undos)
			return i, err
		}
		u := undo{tip: s.tip, fin: s.fin, win: s.win}
		if e.Kind == KindRevert {
			// Later blocks of the batch may overwrite the dropped refs in the backing array.
			if dropped := s.win[s.above(e.To.Number):]; len(dropped) > 0 {
				u.dropped = slices.Clone(dropped)
			}
		}
		undos = append(undos, u)
		s.Apply(*e)
	}
	s.undo(undos)
	return -1, nil
}

// undo is what CheckBatch needs to take back one applied entry.
type undo struct {
	tip, fin chain.BlockRef
	win      []chain.BlockRef // the window's slice header before the entry
	dropped  []chain.BlockRef // a copy of the refs a Revert dropped from the window's end
}

// undo takes back the entries of us, newest first.
func (s *State) undo(us []undo) {
	for i := len(us) - 1; i >= 0; i-- {
		u := &us[i]
		s.tip, s.fin, s.win = u.tip, u.fin, u.win
		copy(s.win[len(s.win)-len(u.dropped):], u.dropped)
	}
}

// Apply records e. It is meant for entries that passed Check; for any other entry it does not
// panic, and the resulting state is unspecified.
func (s *State) Apply(e Entry) {
	switch e.Kind {
	case KindBlock:
		r := e.Block.Ref
		s.tip = r
		if e.Final {
			s.fin = r
			s.win = s.win[:0]
			return
		}
		// append copies only the live refs when it grows, which drops the prefix Finalize
		// re-sliced away.
		s.win = append(s.win, r)
	case KindRevert:
		s.tip = e.To
		s.win = s.win[:s.above(e.To.Number)]
	case KindFinalize:
		s.fin = e.To
		s.win = s.win[s.above(e.To.Number):]
		if isZero(s.tip) || e.To.Number > s.tip.Number {
			s.tip = e.To
		}
	}
}

func (s *State) windowCap() int {
	if s.cap == 0 {
		return DefaultWindowCap
	}
	return s.cap
}

// find returns the index of the window ref numbered n, or the index where it would be and false.
func (s *State) find(n uint64) (int, bool) {
	if len(s.win) == 0 || n < s.win[0].Number {
		return 0, false
	}
	if n > s.win[len(s.win)-1].Number {
		return len(s.win), false
	}
	// In a window Check built, numbers are contiguous, so the ref numbered n is at n - first.
	// Binary search handles the windows with gaps that only Apply builds.
	if d := n - s.win[0].Number; d < uint64(len(s.win)) && s.win[d].Number == n {
		return int(d), true //nolint:gosec // d < len(s.win)
	}
	return slices.BinarySearchFunc(s.win, n, func(r chain.BlockRef, n uint64) int { return cmp.Compare(r.Number, n) })
}

// above returns the index of the first window ref numbered above n.
func (s *State) above(n uint64) int {
	i, ok := s.find(n)
	if ok {
		i++
	}
	return i
}

func (s *State) checkBlock(e *Entry) error {
	r := e.Block.Ref
	if tip := s.tip; !isZero(tip) {
		if r.Number <= tip.Number {
			return malformed("block %d is not above tip %s", r.Number, tip)
		}
		if r.Number-1 == tip.Number {
			if r.Parent != tip.Hash {
				return malformed("block %d: parent %s is not tip hash %s", r.Number, r.Parent.Hex(), tip.Hash.Hex())
			}
		} else if !e.Final {
			return malformed("block %d: non-final block is not the child of tip %s", r.Number, tip)
		}
	}
	if e.Final {
		if len(s.win) > 0 {
			return malformed("block %d: final block with %d unfinalized blocks in the window", r.Number, len(s.win))
		}
		return nil
	}
	if c := s.windowCap(); len(s.win) >= c {
		return malformed("block %d: window is full (%d blocks)", r.Number, c)
	}
	return nil
}

func (s *State) checkRevert(to chain.BlockRef) error {
	if isZero(s.tip) || to.Number >= s.tip.Number {
		return malformed("revert to %s: target is not below tip %s", to, s.tip)
	}
	if to == s.fin {
		return nil // To has a non-zero hash, so it never matches a zero F
	}
	if i, ok := s.find(to.Number); ok && s.win[i] == to {
		return nil
	}
	return malformed("revert to %s: target is neither the finalized block nor in the window", to)
}

func (s *State) checkFinalize(to chain.BlockRef) error {
	if f := s.fin; !isZero(f) {
		if to.Number < f.Number {
			return malformed("finalize to %s: below finalized %s", to, f)
		}
		if to.Number == f.Number {
			if to != f {
				return malformed("finalize to %s: conflicts with finalized %s", to, f)
			}
			return nil // a repeated Finalize
		}
	}
	if i, ok := s.find(to.Number); ok && s.win[i] != to {
		return malformed("finalize to %s: conflicts with window block %s", to, s.win[i])
	}
	if to.Number < math.MaxUint64 {
		if i, ok := s.find(to.Number + 1); ok && s.win[i].Parent != to.Hash {
			return malformed("finalize to %s: window block %s does not link to it", to, s.win[i])
		}
	}
	if t := s.tip; !isZero(t) && to.Number > 0 && to.Number-1 == t.Number && to.Parent != t.Hash {
		return malformed("finalize to %s: parent %s is not tip hash %s", to, to.Parent.Hex(), t.Hash.Hex())
	}
	return nil
}

// checkShape checks the fields of e alone: its kind, the fields its kind leaves zero, non-zero
// hashes and the logs of a block.
func checkShape(e *Entry) error {
	switch e.Kind {
	case KindBlock:
		r := e.Block.Ref
		if !isZero(e.To) {
			return malformed("block %d: To must be zero", r.Number)
		}
		if r.Hash == (common.Hash{}) {
			return malformed("block %d: zero hash", r.Number)
		}
		return checkLogs(r, e.Block.Logs)
	case KindRevert, KindFinalize:
		if !isZero(e.Block.Ref) || len(e.Block.Logs) > 0 || e.Final {
			return malformed("%s entry: Block and Final must be zero", e.Kind)
		}
		if e.To.Hash == (common.Hash{}) {
			return malformed("%s to %d: zero hash", e.Kind, e.To.Number)
		}
		return nil
	default:
		return malformed("unknown kind %d", uint8(e.Kind))
	}
}

func checkLogs(r chain.BlockRef, logs []chain.Log) error {
	for i := range logs {
		l := &logs[i]
		if l.BlockNumber != r.Number {
			return malformed("block %d: log %d: block number %d", r.Number, i, l.BlockNumber)
		}
		if l.BlockHash != r.Hash {
			return malformed("block %d: log %d: block hash %s", r.Number, i, l.BlockHash.Hex())
		}
		if len(l.Topics) > maxTopics {
			return malformed("block %d: log %d: %d topics, at most %d", r.Number, i, len(l.Topics), maxTopics)
		}
		if i == 0 {
			continue
		}
		prev := &logs[i-1]
		if l.Index <= prev.Index {
			return malformed("block %d: log %d: index %d not above %d", r.Number, i, l.Index, prev.Index)
		}
		if l.TxIndex < prev.TxIndex {
			return malformed("block %d: log %d: tx index %d below %d", r.Number, i, l.TxIndex, prev.TxIndex)
		}
	}
	return nil
}

func isZero(r chain.BlockRef) bool { return r == chain.BlockRef{} }

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}
