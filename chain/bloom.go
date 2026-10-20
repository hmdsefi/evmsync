// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"hash"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"
)

// Bloom is the 2048-bit logsBloom of a block header, in header byte order. Test, MayContain and
// IsZero only read b and are safe for concurrent use; Add writes b.
type Bloom [256]byte

// keccak is a reusable Keccak-256 state with its output buffer.
type keccak struct {
	h   hash.Hash
	sum [32]byte
}

// keccakPool keeps Add and Test free of per-call allocations.
var keccakPool = sync.Pool{
	New: func() any { return &keccak{h: sha3.NewLegacyKeccak256()} },
}

// bloomBits returns the byte index and bit mask of the three bloom bits for data, as go-ethereum
// computes them: three 11-bit values from the first six bytes of Keccak-256(data).
func bloomBits(data []byte) (idx [3]int, mask [3]byte) {
	k := keccakPool.Get().(*keccak) //nolint:errcheck // the pool only holds *keccak
	k.h.Reset()
	k.h.Write(data)
	h := k.h.Sum(k.sum[:0])
	for i := range 3 {
		j := 2 * i
		mask[i] = 1 << (h[j+1] & 0x7)
		idx[i] = 255 - int((uint16(h[j])<<8|uint16(h[j+1]))&0x7ff)>>3
	}
	keccakPool.Put(k)
	return idx, mask
}

// Add sets the three bits of data, as Ethereum does for each log address and topic.
func (b *Bloom) Add(data []byte) {
	idx, mask := bloomBits(data)
	for i := range idx {
		b[idx[i]] |= mask[i]
	}
}

// Test reports whether the three bits of data are all set. A false result proves that data was
// never added.
func (b Bloom) Test(data []byte) bool {
	idx, mask := bloomBits(data)
	for i := range idx {
		if b[idx[i]]&mask[i] == 0 {
			return false
		}
	}
	return true
}

// MayContain reports whether a block with bloom b may hold a log that passes f. false proves that
// no log of the block passes f; true may be a false positive. Callers that treat an all-zero bloom
// as "unknown" check IsZero first: MayContain does not.
func (b Bloom) MayContain(f Filter) bool {
	if len(f.Addresses) > 0 && !b.anyAddress(f.Addresses) {
		return false
	}
	for _, pos := range f.Topics {
		if len(pos) > 0 && !b.anyTopic(pos) {
			return false
		}
	}
	return true
}

// anyAddress reports whether Test is true for one of as. It indexes the slice instead of ranging
// over copies, so the hashed bytes do not escape to the heap.
func (b *Bloom) anyAddress(as []common.Address) bool {
	for i := range as {
		if b.Test(as[i][:]) {
			return true
		}
	}
	return false
}

// anyTopic reports whether Test is true for one of ts.
func (b *Bloom) anyTopic(ts []common.Hash) bool {
	for i := range ts {
		if b.Test(ts[i][:]) {
			return true
		}
	}
	return false
}

// IsZero reports whether all 2048 bits are zero.
func (b Bloom) IsZero() bool {
	return b == Bloom{}
}

// BloomOf returns the bloom of logs: Add of every log's address and of each of its topics.
func BloomOf(logs []Log) Bloom {
	var b Bloom
	for i := range logs {
		l := &logs[i]
		b.Add(l.Address[:])
		for j := range l.Topics {
			b.Add(l.Topics[j][:])
		}
	}
	return b
}
