// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"strconv"

	"github.com/ethereum/go-ethereum/common"
)

// BlockRef identifies a block and links it to its parent. Two refs name the same block only when
// their hashes are equal. The zero BlockRef means "no block".
type BlockRef struct {
	Number uint64
	Hash   common.Hash
	Parent common.Hash
	Time   uint64 // unix seconds, from the block header
}

// String returns the decimal number, one space and Hash.Hex(), for example
// "123 0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef".
func (r BlockRef) String() string {
	return strconv.FormatUint(r.Number, 10) + " " + r.Hash.Hex()
}

// Header is the part of a block header evmsync uses.
type Header struct {
	Ref   BlockRef
	Bloom Bloom // the block's logsBloom; all zero when the source does not know it
}

// Log is one event log of a block. Removed logs never appear: a reorg is a chainlog Revert entry.
type Log struct {
	Address     common.Address
	Topics      []common.Hash // 0 to 4 topics; nil when there are none
	Data        []byte        // nil when empty
	BlockNumber uint64        // the number of the block holding the log
	BlockHash   common.Hash   // the hash of the block holding the log
	TxHash      common.Hash
	TxIndex     uint32 // index of the transaction in the block
	Index       uint32 // index of the log in the block
}

// Block is a block reference and the logs of that block that match the ingest filter, in
// increasing Index order. Logs is nil when no log matches.
type Block struct {
	Ref  BlockRef
	Logs []Log
}
