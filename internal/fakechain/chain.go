// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// Params shape a generated chain. A zero field takes its default.
type Params struct {
	ChainID      uint64 // default 31337
	ForkPPM      uint32 // chance that a Step is a reorg; default 150_000
	MaxForkDepth uint64 // deepest Step reorg; default 8
	MaxMine      uint64 // a mining Step adds 1..MaxMine blocks; default 3
	FinalityPPM  uint32 // chance that a Step advances finality; default 500_000
	MinLag       uint64 // finality lag range, inclusive; default 2
	MaxLag       uint64 // default 32
	MoveLogPPM   uint32 // chance that an orphaned log is re-included by the reorg; default 300_000
}

func (p Params) withDefaults() Params {
	def := func(v *uint64, d uint64) {
		if *v == 0 {
			*v = d
		}
	}
	defPPM := func(v *uint32, d uint32) {
		if *v == 0 {
			*v = d
		}
	}
	def(&p.ChainID, 31337)
	defPPM(&p.ForkPPM, 150_000)
	def(&p.MaxForkDepth, 8)
	def(&p.MaxMine, 3)
	defPPM(&p.FinalityPPM, 500_000)
	def(&p.MinLag, 2)
	def(&p.MaxLag, 32)
	defPPM(&p.MoveLogPPM, 300_000)
	return p
}

// node is one block of the tree with all of its logs, unfiltered.
type node struct {
	header chain.Header
	logs   []chain.Log
}

// Chain is an in-memory block tree with a canonical head and a finalized number. The same seed,
// Params and method calls always give the same chain. It is not safe for concurrent use; while a
// Source serves it, change it only through Source.Do.
type Chain struct {
	seed      uint64
	p         Params
	nodes     map[common.Hash]*node // every tree block; for lookup only, never ranged over
	canonical []common.Hash         // canonical[n] is the canonical block at height n
	finalized uint64
	serial    uint64 // serial of the last created block; genesis is 0
	txSerial  uint64 // serial of the last fresh log's transaction

	// lastOrphan is the head the latest reorg of depth >= 1 orphaned (zero before any), and
	// lastFork the number of that reorg's fork point.
	lastOrphan common.Hash
	lastFork   uint64

	chainRand *Rand // Step decisions
	logRand   *Rand // log content and log moves
}

// New returns a chain holding only genesis: number 0, time GenesisTime, no logs, finalized.
func New(seed uint64, p Params) *Chain {
	c := &Chain{
		seed:      seed,
		p:         p.withDefaults(),
		nodes:     map[common.Hash]*node{},
		chainRand: NewRand(seed, "chain"),
		logRand:   NewRand(seed, "logs"),
	}
	ref := chain.BlockRef{Hash: blockHash(0, common.Hash{}, 0), Time: GenesisTime}
	c.nodes[ref.Hash] = &node{header: chain.Header{Ref: ref}}
	c.canonical = []common.Hash{ref.Hash}
	return c
}

// Params returns the parameters with defaults filled in.
func (c *Chain) Params() Params {
	return c.p
}

// Meta returns the chainlog metadata for a log of this chain with filter f: an ID derived from
// the seed, the chain ID, start block 0 and f.
func (c *Chain) Meta(f chain.Filter) chainlog.Meta {
	h := keccak([]byte("evmsync/fakechain/log"), u64be(c.seed))
	var id chainlog.ID
	copy(id[:], h[:len(id)])
	return chainlog.Meta{ID: id, ChainID: c.p.ChainID, Start: 0, Filter: f}
}

// EventKind names a Step's event.
type EventKind uint8

const (
	EventMine  EventKind = iota + 1 // blocks mined on the head
	EventReorg                      // top Depth blocks replaced with Added blocks
)

// Event describes one Step.
type Event struct {
	Kind      EventKind
	Depth     uint64         // EventReorg: the number of canonical blocks replaced
	Added     int            // blocks created
	Head      chain.BlockRef // head after the event
	Finalized uint64         // finalized number after the event
}

// String returns "mine 2 -> head 41 0x1234abcd fin 30" or
// "reorg depth 3 +4 -> head 42 0x9876fedc fin 30", where the hash is the head hash's first 4
// bytes in hex.
func (e Event) String() string {
	if e.Kind == EventReorg {
		return fmt.Sprintf("reorg depth %d +%d -> head %d 0x%x fin %d",
			e.Depth, e.Added, e.Head.Number, e.Head.Hash[:4], e.Finalized)
	}
	return fmt.Sprintf("mine %d -> head %d 0x%x fin %d", e.Added, e.Head.Number, e.Head.Hash[:4], e.Finalized)
}

// Step performs one seeded event and returns it: a reorg that never replaces the finalized block
// (with probability ForkPPM while the head is above it), else mining 1 to MaxMine blocks; then,
// with probability FinalityPPM, finality moves to a lag of MinLag to MaxLag blocks below the head
// when that is above the current finalized number.
func (c *Chain) Step() Event {
	r := c.chainRand
	ev := Event{Kind: EventMine}
	fork := r.Chance(c.p.ForkPPM)
	if head := c.head(); fork && head > c.finalized {
		maxD := min(c.p.MaxForkDepth, head-c.finalized)
		d := uint64(1)
		for d < maxD && r.Chance(500_000) {
			d++
		}
		n := int(d - 1 + r.Intn(3)) //nolint:gosec // at most MaxForkDepth + 1 blocks
		c.Reorg(d, n)
		ev = Event{Kind: EventReorg, Depth: d, Added: n}
	} else {
		n := int(1 + r.Intn(c.p.MaxMine)) //nolint:gosec // at most MaxMine blocks
		c.Mine(n)
		ev.Added = n
	}
	adv := r.Chance(c.p.FinalityPPM)
	lag := c.p.MinLag + r.Intn(c.p.MaxLag-c.p.MinLag+1)
	if head := c.head(); adv && head >= lag && head-lag > c.finalized {
		c.SetFinalized(head - lag)
	}
	ev.Head = c.Head().Ref
	ev.Finalized = c.finalized
	return ev
}

// Mine appends n blocks to the head and returns their refs, ascending. Each block draws fresh
// logs. It panics if n is negative.
func (c *Chain) Mine(n int) []chain.BlockRef {
	if n < 0 {
		panic("evmsync: fakechain: negative block count")
	}
	refs := make([]chain.BlockRef, 0, n)
	parent := c.Head().Ref
	for range n {
		parent = c.create(parent, nil)
		refs = append(refs, parent)
	}
	return refs
}

// Reorg replaces the top depth canonical blocks with n new blocks on the block depth below the
// head, and returns the new refs, ascending. n may be 0 (a rollback). Each orphaned log moves to a
// random new block with probability MoveLogPPM, keeping its address, topics, data and transaction
// hash; the orphans stay in the tree. Unlike Step, Reorg ignores finality, so tests can reorg below
// it; finality then drops to the new head if it was above it. It panics if depth is above the head
// number or n is negative.
func (c *Chain) Reorg(depth uint64, n int) []chain.BlockRef {
	if n < 0 {
		panic("evmsync: fakechain: negative block count")
	}
	head := c.head()
	if depth > head {
		panic(fmt.Sprintf("evmsync: fakechain: reorg depth %d above head %d", depth, head))
	}
	fork := head - depth

	var pool []chain.Log
	for num := fork + 1; num <= head; num++ {
		for _, l := range c.nodes[c.canonical[num]].logs {
			if c.logRand.Chance(c.p.MoveLogPPM) {
				pool = append(pool, l)
			}
		}
	}
	moved := make([][]chain.Log, n)
	if n > 0 {
		for _, l := range pool {
			k := c.logRand.Intn(uint64(n))
			moved[k] = append(moved[k], l)
		}
	}

	if depth >= 1 {
		c.lastOrphan, c.lastFork = c.canonical[head], fork
	}
	c.canonical = c.canonical[:fork+1]
	refs := make([]chain.BlockRef, 0, n)
	parent := c.nodes[c.canonical[fork]].header.Ref
	for i := range n {
		parent = c.create(parent, moved[i])
		refs = append(refs, parent)
	}
	c.finalized = min(c.finalized, c.head())
	return refs
}

// create makes a canonical block on parent with fresh logs followed by moved, and returns its ref.
func (c *Chain) create(parent chain.BlockRef, moved []chain.Log) chain.BlockRef {
	c.serial++
	num := parent.Number + 1
	ref := chain.BlockRef{
		Number: num,
		Hash:   blockHash(num, parent.Hash, c.serial),
		Parent: parent.Hash,
		Time:   GenesisTime + 12*num,
	}
	logs := append(c.freshLogs(), moved...)
	for i := range logs {
		l := &logs[i]
		l.BlockNumber, l.BlockHash = num, ref.Hash
		l.Index, l.TxIndex = uint32(i), uint32(i) //nolint:gosec // a block holds a few logs
	}
	if len(logs) == 0 {
		logs = nil
	}
	c.nodes[ref.Hash] = &node{header: chain.Header{Ref: ref, Bloom: chain.BloomOf(logs)}, logs: logs}
	c.canonical = append(c.canonical, ref.Hash)
	return ref
}

// SetFinalized sets the finalized number. It panics if n is below the current finalized number or
// above the head.
func (c *Chain) SetFinalized(n uint64) {
	if n < c.finalized || n > c.head() {
		panic(fmt.Sprintf("evmsync: fakechain: finalized %d outside [%d, %d]", n, c.finalized, c.head()))
	}
	c.finalized = n
}

// head returns the head's number.
func (c *Chain) head() uint64 {
	return uint64(len(c.canonical) - 1) //nolint:gosec // canonical always holds genesis
}

// Genesis returns the header of block 0.
func (c *Chain) Genesis() chain.Header {
	return c.nodes[c.canonical[0]].header
}

// Head returns the canonical head's header.
func (c *Chain) Head() chain.Header {
	return c.nodes[c.canonical[c.head()]].header
}

// Finalized returns the canonical header at the finalized number.
func (c *Chain) Finalized() chain.Header {
	return c.nodes[c.canonical[c.finalized]].header
}

// Canonical returns the canonical header at height n, and false when n is above the head.
func (c *Chain) Canonical(n uint64) (chain.Header, bool) {
	if n > c.head() {
		return chain.Header{}, false
	}
	return c.nodes[c.canonical[n]].header, true
}

// Header returns any tree block's header, canonical or orphan, and false for an unknown hash.
func (c *Chain) Header(h common.Hash) (chain.Header, bool) {
	nd, ok := c.nodes[h]
	if !ok {
		return chain.Header{}, false
	}
	return nd.header, true
}

// Block returns a tree block with only its logs that match f, in Index order, and false for an
// unknown hash. Logs is nil when no log matches. The result is a copy.
func (c *Chain) Block(h common.Hash, f chain.Filter) (chain.Block, bool) {
	nd, ok := c.nodes[h]
	if !ok {
		return chain.Block{}, false
	}
	return chain.Block{Ref: nd.header.Ref, Logs: matching(nd.logs, f)}, true
}

// CanonicalBlocks returns canonical blocks 1 to the head with their logs that match f, ascending,
// including blocks with no matching log. The result is a copy.
func (c *Chain) CanonicalBlocks(f chain.Filter) []chain.Block {
	blocks := make([]chain.Block, 0, c.head())
	for _, h := range c.canonical[1:] {
		nd := c.nodes[h]
		blocks = append(blocks, chain.Block{Ref: nd.header.Ref, Logs: matching(nd.logs, f)})
	}
	return blocks
}

// Orphaned reports whether h is a tree block that is not canonical.
func (c *Chain) Orphaned(h common.Hash) bool {
	nd, ok := c.nodes[h]
	return ok && !c.isCanonical(nd.header.Ref)
}

func (c *Chain) isCanonical(r chain.BlockRef) bool {
	return r.Number <= c.head() && c.canonical[r.Number] == r.Hash
}
