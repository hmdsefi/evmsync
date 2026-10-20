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

// Finalize processes f, the chain's new finalized block, and returns the entries that
// make the log treat f as final: nothing when f is at or below the current finalized
// number; a single Finalize when f is in the window; and, when f is not in the window,
// the entries of the walk to f followed by Finalize.
//
// On error Finalize returns nil entries and the Tracker's state is unchanged.
func (t *Tracker) Finalize(ctx context.Context, f chain.BlockRef) ([]chainlog.Entry, error) {
	if f.Hash == (common.Hash{}) {
		return nil, fmt.Errorf("%w: zero block hash", ErrInconsistentSource)
	}
	if !t.anchored() {
		t.reset(f)
		return []chainlog.Entry{finalizeEntry(f)}, nil
	}
	if f.Number < t.fin.Number {
		return nil, nil
	}
	if f.Number == t.fin.Number {
		if f.Hash == t.fin.Hash {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: finalized block %d is %s, got %s",
			ErrReorgBelowFinalized, t.fin.Number, t.fin.Hash.Hex(), f.Hash.Hex())
	}
	if stored, ok := t.inWindow(f.Hash); ok {
		if stored.Number != f.Number {
			return nil, fmt.Errorf("%w: block %s is at height %d, got %d",
				ErrInconsistentSource, f.Hash.Hex(), stored.Number, f.Number)
		}
		t.finalizeTo(stored)
		return []chainlog.Entry{finalizeEntry(stored)}, nil
	}
	if err := t.checkCap(f); err != nil {
		return nil, err
	}
	_, entries, err := t.walk(ctx, f, 1)
	if err != nil {
		return nil, err
	}
	t.reset(f)
	return append(entries, finalizeEntry(f)), nil
}

func finalizeEntry(f chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindFinalize, To: f}
}
