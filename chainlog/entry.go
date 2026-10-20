// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"encoding/hex"
	"strconv"

	"github.com/hmdsefi/evmsync/chain"
)

// Offset is an entry's position in a log. The first entry has offset 1; 0 means "before the
// first entry".
type Offset uint64

// Kind is an entry's type.
type Kind uint8

// The three entry kinds.
const (
	KindBlock    Kind = iota + 1 // apply a block
	KindRevert                   // undo every block above To
	KindFinalize                 // every block at or below To is final
)

// String returns "block", "revert", "finalize", or "Kind(<n>)" for other values.
func (k Kind) String() string {
	switch k {
	case KindBlock:
		return "block"
	case KindRevert:
		return "revert"
	case KindFinalize:
		return "finalize"
	default:
		return "Kind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Entry is one entry of the log. Only the fields of its Kind are set; the others are zero.
type Entry struct {
	Offset Offset // assigned by Append; ignored on input
	Kind   Kind
	Block  chain.Block    // KindBlock
	Final  bool           // KindBlock: ingest knew the block was final when it appended it
	To     chain.BlockRef // KindRevert, KindFinalize
}

// ID identifies one log instance. It is random, fixed at creation, and changes when a log is
// deleted and rebuilt, which renumbers offsets.
type ID [16]byte

// String returns the 32 lowercase hex digits of id.
func (id ID) String() string { return hex.EncodeToString(id[:]) }

// IsZero reports whether every byte of id is zero.
func (id ID) IsZero() bool { return id == ID{} }

// Meta is a log's metadata, fixed when the log is created.
type Meta struct {
	ID      ID
	ChainID uint64
	Start   uint64       // the first block ingest indexes
	Filter  chain.Filter // the ingest filter
}

// DefaultWindowCap is the window cap used when a cap of 0 is given.
const DefaultWindowCap = 100_000

// MaxRecordSize is the largest record body, in bytes, that Decode accepts (16 MiB).
const MaxRecordSize = 16 << 20
