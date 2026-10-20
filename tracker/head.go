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
	if err := t.checkCap(r); err != nil {
		return nil, err
	}
	anc, entries, err := t.walk(ctx, r, 0)
	if err != nil {
		return nil, err
	}
	t.follow(anc, entries)
	return entries, nil
}

// walk follows parent hashes from x until a parent is in K, then fetches the logs of the new
// blocks. It returns the ancestor and the entries: a Revert to the ancestor when it is below the
// tip, then one Block per new block in increasing number order. The slice has room for spare
// more entries. walk changes nothing in t.
func (t *Tracker) walk(ctx context.Context, x chain.BlockRef, spare int) (chain.BlockRef, []chainlog.Entry, error) {
	// The new blocks from x down. Most walks add one or two blocks, which fit in buf.
	var buf [4]chain.BlockRef
	refs := append(buf[:0], x)
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
		refs = append(refs, hd.Ref)
		cur = hd.Ref
	}

	n := len(refs) + spare
	revert := anc.Number < t.tip().Number
	if revert {
		n++
	}
	entries := make([]chainlog.Entry, 0, n)
	if revert {
		entries = append(entries, chainlog.Entry{Kind: chainlog.KindRevert, To: anc})
	}
	for i := len(refs) - 1; i >= 0; i-- {
		b := refs[i]
		if err := ctx.Err(); err != nil {
			return anc, nil, fmt.Errorf("tracker: %w", err)
		}
		logs, err := t.src.Logs(ctx, b.Hash, t.filter)
		if err != nil {
			return anc, nil, fmt.Errorf("tracker: logs of block %d %s: %w", b.Number, b.Hash.Hex(), err)
		}
		entries = append(entries, chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: b, Logs: logs}})
	}
	return anc, entries, nil
}

// checkCap returns ErrWindowFull when adding blocks up to x would grow the window past the cap.
// The window is contiguous above F, so it would hold x.Number - F.Number blocks.
func (t *Tracker) checkCap(x chain.BlockRef) error {
	if n := x.Number - t.fin.Number; n > uint64(t.cap) { //nolint:gosec // New rejects a negative cap
		return fmt.Errorf("%w: block %d is %d blocks above finalized block %d (cap %d)",
			ErrWindowFull, x.Number, n, t.fin.Number, t.cap)
	}
	return nil
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
