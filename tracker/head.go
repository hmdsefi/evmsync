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
// Head returns ErrWindowFull, without calling the Source, when following h would grow
// the window past the cap. On error Head returns nil entries and the Tracker's state
// is unchanged. The caller appends the returned entries with one chainlog.Log.Append
// call.
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

// walk follows the branch of x down to K and fetches the logs of its new blocks. It returns the
// ancestor and the entries: a Revert to the ancestor when it is below the tip, then one Block per
// new block in increasing number order, with room for spare more entries. walk changes nothing in
// t.
func (t *Tracker) walk(ctx context.Context, x chain.BlockRef, spare int) (chain.BlockRef, []chainlog.Entry, error) {
	// Most walks add one or two blocks, which fit in buf.
	var buf [4]chain.BlockRef
	anc, refs, err := t.branch(ctx, x, buf[:0])
	if err != nil {
		return chain.BlockRef{}, nil, err
	}
	entries, err := t.withLogs(ctx, anc, refs, spare)
	if err != nil {
		return chain.BlockRef{}, nil, err
	}
	return anc, entries, nil
}

// branch follows parent hashes from x until a parent is in K, appending x and every fetched ref to
// refs, highest first. It returns that K ref, the ancestor, and refs.
func (t *Tracker) branch(ctx context.Context, x chain.BlockRef, refs []chain.BlockRef) (chain.BlockRef, []chain.BlockRef, error) {
	refs = append(refs, x)
	// seen maps the hashes in refs to their numbers. Extending the tip never fetches, so it is
	// built only once a fetch is needed.
	var seen map[common.Hash]uint64
	cur := x
	for {
		if p, ok := t.known(cur.Parent); ok {
			if p.Number+1 != cur.Number {
				return chain.BlockRef{}, nil, fmt.Errorf("%w: block %d %s has parent %s at height %d",
					ErrInconsistentSource, cur.Number, cur.Hash.Hex(), p.Hash.Hex(), p.Number)
			}
			return p, refs, nil
		}
		if cur.Number-1 <= t.fin.Number {
			return chain.BlockRef{}, nil, fmt.Errorf("%w: block %d %s does not descend from finalized block %d %s",
				ErrReorgBelowFinalized, cur.Number, cur.Hash.Hex(), t.fin.Number, t.fin.Hash.Hex())
		}
		if cur.Parent == (common.Hash{}) {
			return chain.BlockRef{}, nil, fmt.Errorf("%w: block %d %s has a zero parent hash",
				ErrInconsistentSource, cur.Number, cur.Hash.Hex())
		}
		if seen == nil {
			seen = map[common.Hash]uint64{x.Hash: x.Number}
		}
		if n, ok := seen[cur.Parent]; ok {
			return chain.BlockRef{}, nil, fmt.Errorf("%w: block %d %s has parent %s, which is block %d of the same branch",
				ErrInconsistentSource, cur.Number, cur.Hash.Hex(), cur.Parent.Hex(), n)
		}
		if err := ctx.Err(); err != nil {
			return chain.BlockRef{}, nil, fmt.Errorf("tracker: %w", err)
		}
		hd, err := t.src.HeaderByHash(ctx, cur.Parent)
		if err != nil {
			return chain.BlockRef{}, nil, fmt.Errorf("tracker: header %s (block %d): %w", cur.Parent.Hex(), cur.Number-1, err)
		}
		if hd.Ref.Hash != cur.Parent {
			return chain.BlockRef{}, nil, fmt.Errorf("%w: header %s: got hash %s",
				ErrInconsistentSource, cur.Parent.Hex(), hd.Ref.Hash.Hex())
		}
		if hd.Ref.Number != cur.Number-1 {
			return chain.BlockRef{}, nil, fmt.Errorf("%w: header %s: got number %d, want %d",
				ErrInconsistentSource, cur.Parent.Hex(), hd.Ref.Number, cur.Number-1)
		}
		refs = append(refs, hd.Ref)
		seen[hd.Ref.Hash] = hd.Ref.Number
		cur = hd.Ref
	}
}

// withLogs fetches the logs of refs, lowest first, and returns the entries of a walk that ends at
// anc: a Revert to anc when it is below the tip, then one Block per ref in increasing number
// order. The slice holds exactly those entries plus room for spare more.
func (t *Tracker) withLogs(ctx context.Context, anc chain.BlockRef, refs []chain.BlockRef, spare int) ([]chainlog.Entry, error) {
	revert := anc.Number < t.tip().Number
	n := len(refs) + spare
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
			return nil, fmt.Errorf("tracker: %w", err)
		}
		logs, err := t.src.Logs(ctx, b.Hash, t.filter)
		if err != nil {
			return nil, fmt.Errorf("tracker: logs of block %d %s: %w", b.Number, b.Hash.Hex(), err)
		}
		entries = append(entries, chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: b, Logs: logs}})
	}
	return entries, nil
}

// checkCap returns ErrWindowFull when adding blocks up to x would grow the window past the cap.
// The window is contiguous above F, so it would hold x.Number - F.Number blocks. x must be above
// F.
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
