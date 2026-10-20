// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// Memory is a Consumer for in-memory state machines that cannot undo their writes. It keeps a
// snapshot of the state at the latest Finalize and the pending blocks since; Revert restores the
// snapshot and re-applies the pending blocks up to the target. Its committed offset lives in
// memory: a new Memory starts at offset 0 and replays the log from offset 1.
//
// Memory is safe for concurrent use: State and Offset may be called while Run drives Apply.
type Memory[S any] struct {
	apply func(s S, b chain.Block) S
	clone func(s S) S

	mu  sync.Mutex
	cur S // the current state; guarded by mu
	// snap is the state at base, valid only while blocks are pending; guarded by mu.
	snap S
	// pending[first:] are the non-final blocks applied since base, ascending by number; the
	// dead prefix pending[:first] is zeroed. Guarded by mu.
	pending []chain.Block
	first   int             // the index of the oldest live pending block; guarded by mu
	base    chain.BlockRef  // the ref below the pending blocks; guarded by mu
	off     chainlog.Offset // the committed offset; guarded by mu
}

var _ Consumer = (*Memory[struct{}])(nil)

// NewMemory returns a Memory with state initial and committed offset 0.
//
// apply returns the state after applying block b to s. It must be deterministic and may modify
// s and return it: Memory owns every S it passes. clone returns a deep copy that shares no
// mutable memory with its argument. NewMemory panics if apply or clone is nil. Memory never
// recovers a panic from apply or clone; after one, the Memory must not be used.
func NewMemory[S any](initial S, apply func(s S, b chain.Block) S, clone func(s S) S) *Memory[S] {
	if apply == nil {
		panic("evmsync: consumer.NewMemory: nil apply function")
	}
	if clone == nil {
		panic("evmsync: consumer.NewMemory: nil clone function")
	}
	return &Memory[S]{apply: apply, clone: clone, cur: initial}
}

// Offset returns the committed offset. It never fails.
func (m *Memory[S]) Offset(context.Context) (chainlog.Offset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.off, nil
}

// Apply implements Consumer. It returns an error wrapping ErrBadEntry, and changes nothing, when
// e does not fit the state: its offset is not the committed offset plus one, a Revert targets
// neither the base nor a pending block, a Finalize conflicts with a pending block, or its kind
// is unknown.
func (m *Memory[S]) Apply(_ context.Context, e chainlog.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.Offset != m.off+1 {
		return fmt.Errorf("%w: offset %d, want %d", ErrBadEntry, e.Offset, m.off+1)
	}
	switch e.Kind {
	case chainlog.KindBlock:
		m.block(e.Block, e.Final)
	case chainlog.KindRevert:
		i, ok := m.revertTarget(e.To)
		if !ok {
			return fmt.Errorf("%w: revert target %d %s is not an unfinalized applied block", ErrBadEntry, e.To.Number, e.To.Hash.Hex())
		}
		m.revert(i)
	case chainlog.KindFinalize:
		live := m.live()
		k := sort.Search(len(live), func(i int) bool { return live[i].Ref.Number > e.To.Number })
		if k > 0 && live[k-1].Ref.Number == e.To.Number && live[k-1].Ref.Hash != e.To.Hash {
			return fmt.Errorf("%w: finalize %d %s conflicts with applied block %s", ErrBadEntry, e.To.Number, e.To.Hash.Hex(), live[k-1].Ref.Hash.Hex())
		}
		m.finalize(k, e.To)
	default:
		return fmt.Errorf("%w: unknown kind %d", ErrBadEntry, e.Kind)
	}
	m.off = e.Offset
	return nil
}

// State returns a clone of the current state. The caller owns the result.
func (m *Memory[S]) State() S {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clone(m.cur)
}

// block applies b. A final block with nothing pending becomes the base without a snapshot; with
// blocks pending, which a log never sends, it is applied as a non-final block.
func (m *Memory[S]) block(b chain.Block, final bool) {
	switch {
	case len(m.live()) == 0 && final:
		m.cur = m.apply(m.cur, b)
		m.base = b.Ref
	case len(m.live()) == 0:
		m.snap = m.clone(m.cur)
		m.cur = m.apply(m.cur, b)
		m.pending = append(m.pending, b)
	default:
		m.cur = m.apply(m.cur, b)
		m.pending = append(m.pending, b)
	}
}

// revertTarget returns the index in the pending blocks of the block with to's number and hash,
// or -1 when to is the base, and false when it is neither.
func (m *Memory[S]) revertTarget(to chain.BlockRef) (int, bool) {
	if to.Number == m.base.Number && to.Hash == m.base.Hash {
		return -1, true
	}
	live := m.live()
	for i := len(live) - 1; i >= 0 && live[i].Ref.Number >= to.Number; i-- {
		if live[i].Ref.Number == to.Number {
			return i, live[i].Ref.Hash == to.Hash
		}
	}
	return -1, false
}

// revert makes the state the one at pending block i, or at the base when i is -1.
func (m *Memory[S]) revert(i int) {
	live := m.live()
	switch {
	case i == len(live)-1:
		// The target is the newest block, or the base with nothing pending: nothing changes.
	case i < 0:
		m.cur = m.snap
		m.clearPending()
	default:
		m.cur = m.clone(m.snap)
		for _, b := range live[:i+1] {
			m.cur = m.apply(m.cur, b)
		}
		end := m.first + i + 1
		clear(m.pending[end:])
		m.pending = m.pending[:end]
	}
}

// finalize makes the first k pending blocks final and moves the base to to when it is not below
// the base.
func (m *Memory[S]) finalize(k int, to chain.BlockRef) {
	if live := m.live(); k == len(live) {
		m.clearPending()
	} else {
		for _, b := range live[:k] {
			m.snap = m.apply(m.snap, b)
		}
		clear(live[:k])
		m.first += k
		// Copy the live blocks down once the dead prefix fills half the array, so appends reuse it.
		if m.first > cap(m.pending)/2 {
			n := copy(m.pending, m.live())
			clear(m.pending[n:])
			m.pending = m.pending[:n]
			m.first = 0
		}
	}
	if to.Number >= m.base.Number {
		m.base = to
	}
}

// live returns the pending blocks.
func (m *Memory[S]) live() []chain.Block { return m.pending[m.first:] }

// clearPending drops every pending block and the snapshot.
func (m *Memory[S]) clearPending() {
	var zero S
	m.snap = zero
	clear(m.pending)
	m.pending = m.pending[:0]
	m.first = 0
}
