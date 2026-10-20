// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
)

// Source is the read-only view of one chain that the tracker and ingest use. Implementations are
// safe for concurrent use.
//
// Every implementation follows this contract, and callers may rely on it:
//
//   - A method that fails because the block or tag does not exist returns an error wrapping
//     ErrNotFound; because the chain lacks a feature, ErrUnsupported; because from > to,
//     ErrInvalidRange. Other failures (transport, provider errors, decoding) wrap none of the three.
//   - When ctx is done before or during a call, the method returns an error that wraps ctx.Err()
//     and none of ErrNotFound, ErrUnsupported and ErrInvalidRange.
//   - A source answers from its current view, which can change between two calls (a reorg, a load
//     balancer). Two Latest calls may return decreasing numbers, and Finalized may move backwards.
//   - Results follow the "empty is nil" rule (nil Topics, Data and Logs when empty), give every log
//     the BlockNumber and BlockHash of the block that holds it, list a block's logs in strictly
//     increasing Index order with TxIndex non-decreasing, and belong to the caller: mutating a
//     returned slice never changes a later result.
//   - Every method that takes a Filter treats it as read-only and accepts every filter that
//     Filter.Validate accepts. A source may return an error for a filter that Validate rejects.
type Source interface {
	// Latest returns the head block as the source sees it now. Its error never wraps ErrNotFound.
	Latest(ctx context.Context) (Header, error)
	// Finalized returns the newest finalized block. It returns an error wrapping ErrUnsupported
	// when the chain has no finalized tag, and ErrNotFound when no block is finalized yet.
	Finalized(ctx context.Context) (Header, error)
	// HeaderByNumber returns the canonical header at number n, or an error wrapping ErrNotFound
	// when n is above the source's head. Number 0 is the genesis block.
	HeaderByNumber(ctx context.Context, n uint64) (Header, error)
	// HeaderByHash returns the header of the block with hash h, canonical or not, or an error
	// wrapping ErrNotFound when the source does not know h. The zero hash is never known.
	HeaderByHash(ctx context.Context, h common.Hash) (Header, error)
	// Logs returns the logs of the block with hash block that pass f, in increasing Index order
	// (EIP-234). It returns nil when none passes, and an error wrapping ErrNotFound when the source
	// does not know the block. For a known block that is not canonical, it may return the block's
	// logs or ErrNotFound.
	Logs(ctx context.Context, block common.Hash, f Filter) ([]Log, error)
	// LogsInRange returns the logs of the canonical blocks from..to (inclusive) that pass f, in
	// increasing (BlockNumber, Index) order, or nil when none passes. It returns an error wrapping
	// ErrInvalidRange when from > to, without contacting a provider, and ErrNotFound when to is
	// above the source's head. The blocks are canonical in one view of the source; callers that
	// need more check the log block hashes against headers.
	LogsInRange(ctx context.Context, from, to uint64, f Filter) ([]Log, error)
}

var (
	// ErrNotFound reports that no block has the requested number or hash.
	ErrNotFound = errors.New("chain: not found")
	// ErrUnsupported reports that the chain does not support a request, for example the
	// finalized tag.
	ErrUnsupported = errors.New("chain: unsupported")
	// ErrInvalidRange reports a block range whose start is above its end.
	ErrInvalidRange = errors.New("chain: invalid block range")
	// ErrInvalidFilter reports a filter that breaks the limits of Filter.Validate.
	ErrInvalidFilter = errors.New("chain: invalid filter")
)
