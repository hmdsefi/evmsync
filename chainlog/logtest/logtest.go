// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package logtest

import (
	"encoding/binary"
	"testing"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// WindowCap is the window cap every log under test must have.
const WindowCap = 8

// Meta returns the metadata every log under test must be created with: ID
// 000102030405060708090a0b0c0d0e0f, ChainID 31337, Start 100, Filter with one address
// 0x00000000000000000000000000000000000000aa and Topics [[0x00…01]] (last byte 1).
func Meta() chainlog.Meta {
	m := chainlog.Meta{
		ID:      chainlog.ID{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f},
		ChainID: 31337,
		Start:   100,
	}
	m.Filter.Addresses = append(m.Filter.Addresses, [20]byte{19: 0xaa})
	m.Filter.Topics = append(m.Filter.Topics, nil)
	m.Filter.Topics[0] = append(m.Filter.Topics[0], [32]byte{31: 0x01})
	return m
}

// Run runs the conformance suite. open must return a new, empty log created with Meta() and
// WindowCap; Run calls it once per subtest (twice in the subtests that need a second empty log)
// and closes each log when the subtest ends.
func Run(t *testing.T, open func(t *testing.T) chainlog.Log) {
	t.Helper()
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			fresh := func() chainlog.Log {
				l := open(t)
				t.Cleanup(func() { _ = l.Close() })
				return l
			}
			c.run(t, fresh(), fresh)
		})
	}
}

// h returns the hash with byte 0 = 0xb0|f, bytes 24..31 = n big-endian and all others zero.
func h(n uint64, f byte) [32]byte {
	var x [32]byte
	x[0] = 0xb0 | f
	binary.BigEndian.PutUint64(x[24:], n)
	return x
}

// blk returns block n of chain f, linked to block n-1 of the same chain (no parent for n = 0).
func blk(n uint64, f byte) chain.BlockRef {
	r := chain.BlockRef{Number: n, Hash: h(n, f), Time: 1_700_000_000 + 12*n}
	if n > 0 {
		r.Parent = h(n-1, f)
	}
	return r
}

// fork returns blk(n, f) with the parent of chain base: the first block of a fork off base.
func fork(n uint64, f, base byte) chain.BlockRef {
	r := blk(n, f)
	r.Parent = h(n-1, base)
	return r
}
