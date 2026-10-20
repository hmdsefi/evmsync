// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// Head processes the new head h and returns the entries that make the log follow it:
// nothing for a duplicate, lagging or behind-the-tip head; otherwise an optional
// Revert followed by one Block per new block in increasing number order.
//
// On error Head returns nil entries and the Tracker's state is unchanged. The caller
// appends the returned entries with one chainlog.Log.Append call.
func (t *Tracker) Head(ctx context.Context, h chain.Header) ([]chainlog.Entry, error) {
	r := h.Ref
	if r.Hash == (common.Hash{}) {
		return nil, fmt.Errorf("%w: zero block hash", ErrInconsistentSource)
	}
	if !t.anchored() {
		return nil, fmt.Errorf("%w: head %d %s", ErrUnanchored, r.Number, r.Hash.Hex())
	}
	if _, ok := t.known(r.Hash); ok {
		return nil, nil
	}
	if r.Number <= t.fin.Number || r.Number < t.tip().Number {
		return nil, nil
	}
	anc, entries, err := t.walk(ctx, r)
	if err != nil {
		return nil, err
	}
	t.follow(anc, entries)
	return entries, nil
}

// walk follows parent hashes from x until a parent is in K, then fetches the logs of the new
// blocks. It returns the ancestor and the entries: a Revert to the ancestor when it is below the
// tip, then one Block per new block in increasing number order. It changes nothing in t.
func (t *Tracker) walk(ctx context.Context, x chain.BlockRef) (chain.BlockRef, []chainlog.Entry, error) {
	// Collect the new blocks from x down; a slot is left in front for a Revert.
	entries := make([]chainlog.Entry, 1, 3)
	entries = append(entries, blockEntry(x))
	cur := x
	var anc chain.BlockRef
	for {
		if p, ok := t.known(cur.Parent); ok {
			if p.Number+1 != cur.Number {
				return anc, nil, fmt.Errorf("%w: block %d %s has parent %s at height %d",
					ErrInconsistentSource, cur.Number, cur.Hash.Hex(), p.Hash.Hex(), p.Number)
			}
			anc = p
			break
		}
		if cur.Number-1 <= t.fin.Number {
			return anc, nil, fmt.Errorf("%w: block %d %s does not descend from finalized block %d %s",
				ErrReorgBelowFinalized, cur.Number, cur.Hash.Hex(), t.fin.Number, t.fin.Hash.Hex())
		}
		if err := ctx.Err(); err != nil {
			return anc, nil, fmt.Errorf("tracker: %w", err)
		}
		hd, err := t.src.HeaderByHash(ctx, cur.Parent)
		if err != nil {
			return anc, nil, fmt.Errorf("tracker: header %s (block %d): %w", cur.Parent.Hex(), cur.Number-1, err)
		}
		if hd.Ref.Hash != cur.Parent {
			return anc, nil, fmt.Errorf("%w: header %s: got hash %s",
				ErrInconsistentSource, cur.Parent.Hex(), hd.Ref.Hash.Hex())
		}
		if hd.Ref.Number != cur.Number-1 {
			return anc, nil, fmt.Errorf("%w: header %s: got number %d, want %d",
				ErrInconsistentSource, cur.Parent.Hex(), hd.Ref.Number, cur.Number-1)
		}
		entries = append(entries, blockEntry(hd.Ref))
		cur = hd.Ref
	}

	blocks := entries[1:]
	for i, j := 0, len(blocks)-1; i < j; i, j = i+1, j-1 {
		blocks[i], blocks[j] = blocks[j], blocks[i]
	}
	for i := range blocks {
		b := blocks[i].Block.Ref
		if err := ctx.Err(); err != nil {
			return anc, nil, fmt.Errorf("tracker: %w", err)
		}
		logs, err := t.src.Logs(ctx, b.Hash, t.filter)
		if err != nil {
			return anc, nil, fmt.Errorf("tracker: logs of block %d %s: %w", b.Number, b.Hash.Hex(), err)
		}
		blocks[i].Block.Logs = logs
	}

	if anc.Number < t.tip().Number {
		entries[0] = chainlog.Entry{Kind: chainlog.KindRevert, To: anc}
		return anc, entries, nil
	}
	return anc, blocks, nil
}

func blockEntry(r chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r}}
}

// follow applies the result of a successful walk: the window up to anc, then the new blocks.
func (t *Tracker) follow(anc chain.BlockRef, entries []chainlog.Entry) {
	t.cut(anc.Number)
	for i := range entries {
		if entries[i].Kind == chainlog.KindBlock {
			t.push(entries[i].Block.Ref)
		}
	}
}
