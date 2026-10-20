// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

// Faults configures provider misbehavior. The zero value is an honest provider.
type Faults struct {
	StaleHeadPPM         uint32 // Latest answers a canonical block 1..StaleDepth below the head
	StaleDepth           uint64 // default 3
	StaleOrphanPPM       uint32 // Latest answers the head the latest reorg orphaned
	DropOrphans          bool   // HeaderByHash and Logs answer ErrNotFound for orphans
	FinalizedUnsupported bool   // Finalized answers chain.ErrUnsupported
	DroppedLogsPPM       uint32 // Logs answers no logs for a block that has matching ones
	InterleavePPM        uint32 // a lookup runs Chain.Step before answering
}

// Call records one Source call.
type Call struct {
	Method string      // "Latest", "Finalized", "HeaderByNumber", "HeaderByHash", "Logs", "LogsInRange"
	Hash   common.Hash // HeaderByHash, Logs
	Number uint64      // HeaderByNumber; LogsInRange: from
	To     uint64      // LogsInRange
}

// Source implements chain.Source over a Chain, with the provider faults of Faults. It is safe for
// concurrent use: every method holds one mutex, which also guards the Chain.
type Source struct {
	mu sync.Mutex
	// mu guards every field below and the chain c.
	c      *Chain
	faults Faults
	calls  []Call

	staleRand      *Rand // faults/stale
	orphanRand     *Rand // faults/orphan
	interleaveRand *Rand // faults/interleave
	logsRand       *Rand // faults/logs
}

var _ chain.Source = (*Source)(nil)

// NewSource returns a Source over c whose fault streams derive from seed.
func NewSource(c *Chain, seed uint64, f Faults) *Source {
	return &Source{
		c:              c,
		faults:         f,
		staleRand:      NewRand(seed, "faults/stale"),
		orphanRand:     NewRand(seed, "faults/orphan"),
		interleaveRand: NewRand(seed, "faults/interleave"),
		logsRand:       NewRand(seed, "faults/logs"),
	}
}

// Latest returns the head header. With StaleOrphanPPM it may answer the head the latest reorg
// orphaned, as long as that reorg's fork point is not below the finalized number; with
// StaleHeadPPM, a canonical block 1 to StaleDepth below the head.
func (s *Source) Latest(ctx context.Context) (chain.Header, error) {
	if err := ctx.Err(); err != nil {
		return chain.Header{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Method: "Latest"})
	depth := s.faults.StaleDepth
	if depth == 0 {
		depth = 3
	}
	orphan := s.orphanRand.Chance(s.faults.StaleOrphanPPM)
	stale := s.staleRand.Chance(s.faults.StaleHeadPPM)
	k := 1 + s.staleRand.Intn(depth)

	c := s.c
	if orphan && c.lastOrphan != (common.Hash{}) && c.lastFork >= c.finalized {
		return c.nodes[c.lastOrphan].header, nil
	}
	if stale {
		h, _ := c.Canonical(c.head() - min(k, c.head()))
		return h, nil
	}
	return c.Head(), nil
}

// Finalized returns the canonical header at the chain's finalized number, or an error wrapping
// chain.ErrUnsupported with FinalizedUnsupported.
func (s *Source) Finalized(ctx context.Context) (chain.Header, error) {
	if err := ctx.Err(); err != nil {
		return chain.Header{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Method: "Finalized"})
	if s.faults.FinalizedUnsupported {
		return chain.Header{}, fmt.Errorf("fakechain: finalized: %w", chain.ErrUnsupported)
	}
	return s.c.Finalized(), nil
}

// HeaderByNumber returns the canonical header at n, or an error wrapping chain.ErrNotFound when n
// is above the head.
func (s *Source) HeaderByNumber(ctx context.Context, n uint64) (chain.Header, error) {
	if err := ctx.Err(); err != nil {
		return chain.Header{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookup(Call{Method: "HeaderByNumber", Number: n})
	h, ok := s.c.Canonical(n)
	if !ok {
		return chain.Header{}, numberNotFound(n)
	}
	return h, nil
}

// HeaderByHash returns any tree block's header, or an error wrapping chain.ErrNotFound for an
// unknown hash or, with DropOrphans, an orphan.
func (s *Source) HeaderByHash(ctx context.Context, h common.Hash) (chain.Header, error) {
	if err := ctx.Err(); err != nil {
		return chain.Header{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookup(Call{Method: "HeaderByHash", Hash: h})
	nd, err := s.node(h)
	if err != nil {
		return chain.Header{}, err
	}
	return nd.header, nil
}

// Logs returns the block's logs that pass f, in Index order, or nil when none passes. It returns
// an error wrapping chain.ErrNotFound for an unknown hash or, with DropOrphans, an orphan. With
// DroppedLogsPPM it may answer nil for a block that has matching logs.
func (s *Source) Logs(ctx context.Context, block common.Hash, f chain.Filter) ([]chain.Log, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookup(Call{Method: "Logs", Hash: block})
	nd, err := s.node(block)
	if err != nil {
		return nil, err
	}
	logs := matching(nd.logs, f)
	if s.logsRand.Chance(s.faults.DroppedLogsPPM) {
		return nil, nil
	}
	return logs, nil
}

// LogsInRange returns the logs of canonical blocks from to to (inclusive) that pass f, by block
// then Index, or nil when none passes. It returns an error wrapping chain.ErrInvalidRange when
// from > to, and chain.ErrNotFound when to is above the head.
func (s *Source) LogsInRange(ctx context.Context, from, to uint64, f chain.Filter) ([]chain.Log, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookup(Call{Method: "LogsInRange", Number: from, To: to})
	if from > to {
		return nil, fmt.Errorf("fakechain: logs in range: from %d > to %d: %w", from, to, chain.ErrInvalidRange)
	}
	c := s.c
	if to > c.head() {
		return nil, numberNotFound(to)
	}
	var logs []chain.Log
	for n := from; n <= to; n++ {
		logs = append(logs, matching(c.nodes[c.canonical[n]].logs, f)...)
	}
	return logs, nil
}

// Do runs fn with the Source's mutex held, so tests can change the chain while the Source is in
// use.
func (s *Source) Do(fn func(c *Chain)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.c)
}

// SetFaults replaces the faults. The fault streams keep their positions.
func (s *Source) SetFaults(f Faults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = f
}

// Calls returns a copy of the calls recorded since creation or the last ResetCalls, in order.
// Calls with a context that was already done are not recorded.
func (s *Source) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// ResetCalls clears the call record.
func (s *Source) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// lookup records a lookup call and, with probability InterleavePPM, moves the chain by one Step
// before the call answers. The caller holds s.mu.
func (s *Source) lookup(call Call) {
	s.calls = append(s.calls, call)
	if s.interleaveRand.Chance(s.faults.InterleavePPM) {
		s.c.Step()
	}
}

// node returns the tree block h as the source serves it. The caller holds s.mu.
func (s *Source) node(h common.Hash) (*node, error) {
	nd, ok := s.c.nodes[h]
	if !ok || (s.faults.DropOrphans && !s.c.isCanonical(nd.header.Ref)) {
		return nil, fmt.Errorf("fakechain: block %s: %w", h.Hex(), chain.ErrNotFound)
	}
	return nd, nil
}

func numberNotFound(n uint64) error {
	return fmt.Errorf("fakechain: block %d: %w", n, chain.ErrNotFound)
}
