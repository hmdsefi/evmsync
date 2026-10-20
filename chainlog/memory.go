// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// Memory is a Log held in memory. It never truncates on its own. It is safe for concurrent use.
//
// Memory stores each entry as its encoded record, so it rejects exactly the entries a file log
// rejects, and every Read returns fresh copies.
type Memory struct {
	meta Meta // normalized; never changes

	mu      sync.Mutex
	recs    [][]byte      // the records of offsets oldest..head; guarded by mu
	oldest  Offset        // guarded by mu
	head    Offset        // guarded by mu
	lastFin Offset        // offset of the newest Finalize entry, 0 if none; guarded by mu
	state   State         // guarded by mu
	closed  bool          // guarded by mu
	wake    chan struct{} // closed and replaced by every Append and by Close; guarded by mu
}

var _ Log = (*Memory)(nil)

// NewMemory returns an empty Memory log with metadata meta and window cap cap (0 means
// DefaultWindowCap). A zero meta.ID is replaced by 16 bytes from crypto/rand. It panics if
// cap < 0 or meta.Filter.Validate fails.
func NewMemory(meta Meta, cap int) *Memory {
	if cap < 0 {
		panic("evmsync: chainlog.NewMemory: cap must not be negative, got " + strconv.Itoa(cap))
	}
	if err := meta.Filter.Validate(); err != nil {
		panic("evmsync: chainlog.NewMemory: " + err.Error())
	}
	meta = normalizeMeta(meta)
	if meta.ID.IsZero() {
		_, _ = rand.Read(meta.ID[:]) // never fails since Go 1.24
	}
	return &Memory{meta: meta, oldest: 1, state: NewState(cap), wake: make(chan struct{})}
}

// normalizeMeta returns a deep copy of m whose empty filter lists are nil.
func normalizeMeta(m Meta) Meta {
	m.Filter.Addresses = cloneOrNil(m.Filter.Addresses)
	if len(m.Filter.Topics) == 0 {
		m.Filter.Topics = nil
		return m
	}
	topics := make([][]common.Hash, len(m.Filter.Topics))
	for i, pos := range m.Filter.Topics {
		topics[i] = cloneOrNil(pos)
	}
	m.Filter.Topics = topics
	return m
}

func cloneOrNil[S ~[]E, E any](s S) S {
	if len(s) == 0 {
		return nil
	}
	return slices.Clone(s)
}

// check returns ErrClosed or ctx.Err(), in that order. The caller holds m.mu.
func (m *Memory) check(ctx context.Context) error {
	if m.closed {
		return ErrClosed
	}
	return ctx.Err()
}

// Meta returns the log's metadata.
func (m *Memory) Meta(ctx context.Context) (Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return Meta{}, err
	}
	return normalizeMeta(m.meta), nil
}

// Append validates entries in order against the log's State and appends all of them, or none
// with an error wrapping ErrMalformed. It assigns offsets Head+1, Head+2, ... and returns the
// last one; with no entries it returns Head.
func (m *Memory) Append(ctx context.Context, entries ...Entry) (Offset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return m.head, nil
	}
	if i, err := m.state.CheckBatch(entries); err != nil {
		return 0, fmt.Errorf("chainlog: append entry %d of %d: %w", i+1, len(entries), err)
	}
	for i := range entries {
		e := entries[i]
		m.head++
		e.Offset = m.head
		m.recs = append(m.recs, Encode(make([]byte, 0, headerSize+recordBodySize(e)), e))
		m.state.Apply(e)
		if e.Kind == KindFinalize {
			m.lastFin = e.Offset
		}
	}
	close(m.wake)
	m.wake = make(chan struct{})
	return m.head, nil
}

// Read returns up to max entries starting at offset from, in offset order. It returns nil when
// from is Head+1, an error wrapping ErrTruncated when from is below Oldest, and an error wrapping
// ErrAhead when from is above Head+1. A from of 0 reads from 1. It panics if max < 1.
func (m *Memory) Read(ctx context.Context, from Offset, max int) ([]Entry, error) {
	recs, err := m.records(ctx, from, max)
	if len(recs) == 0 {
		return nil, err
	}
	// Records never change once appended, so they are decoded without the lock.
	es := make([]Entry, len(recs))
	for i, rec := range recs {
		e, _, err := Decode(rec)
		if err != nil {
			// Unreachable: every record was encoded by Append.
			return nil, fmt.Errorf("chainlog: read record %d of %d: %w", i+1, len(recs), err)
		}
		es[i] = e
	}
	return es, nil
}

// records returns the records Read decodes. It copies the slots, since Truncate clears them.
func (m *Memory) records(ctx context.Context, from Offset, max int) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return nil, err
	}
	if max < 1 {
		panic("evmsync: chainlog: Read max must be at least 1, got " + strconv.Itoa(max))
	}
	if from == 0 {
		from = 1
	}
	switch {
	case from < m.oldest:
		return nil, fmt.Errorf("%w: read from %d, oldest is %d", ErrTruncated, from, m.oldest)
	case from == m.head+1:
		return nil, nil
	case from > m.head+1:
		return nil, fmt.Errorf("%w: read from %d, head is %d", ErrAhead, from, m.head)
	}
	n := min(uint64(max), uint64(m.head-from)+1)
	return slices.Clone(m.recs[from-m.oldest:][:n]), nil
}

// Wait returns nil once Head > after, ctx.Err() if ctx ends first, ErrClosed if the log closes
// first, and an error wrapping ErrAhead when after > Head.
func (m *Memory) Wait(ctx context.Context, after Offset) error {
	for {
		m.mu.Lock()
		if err := m.check(ctx); err != nil {
			m.mu.Unlock()
			return err
		}
		head, wake := m.head, m.wake
		m.mu.Unlock()
		switch {
		case head > after:
			return nil
		case after > head:
			return fmt.Errorf("%w: wait after %d, head is %d", ErrAhead, after, head)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

// Head returns the offset of the newest entry, 0 if none was ever appended.
func (m *Memory) Head(ctx context.Context) (Offset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return 0, err
	}
	return m.head, nil
}

// Oldest returns the offset of the first retained entry, Head+1 when none is retained.
func (m *Memory) Oldest(ctx context.Context) (Offset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return 0, err
	}
	return m.oldest, nil
}

// State returns a copy of the validator state after the newest entry.
func (m *Memory) State(ctx context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return State{}, err
	}
	return m.state.Clone(), nil
}

// Truncate drops the retained entries with offsets below min(before, L+1), where L is the offset
// of the newest Finalize entry (0 if none), and releases their memory. If that bound is above
// Oldest, Oldest becomes the bound.
func (m *Memory) Truncate(ctx context.Context, before Offset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return err
	}
	b := min(before, m.lastFin+1)
	if b <= m.oldest {
		return nil
	}
	drop := b - m.oldest
	clear(m.recs[:drop])
	m.recs = m.recs[drop:]
	m.oldest = b
	return nil
}

// Close releases the log's memory and wakes every Wait with ErrClosed. Later calls of other
// methods return ErrClosed; later calls of Close return nil.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	m.recs = nil
	m.state = State{}
	close(m.wake)
	return nil
}
