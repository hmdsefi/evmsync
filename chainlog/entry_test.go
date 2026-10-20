// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"encoding/binary"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

// h returns the hash with byte 0 = 0xb0|f, bytes 24..31 = n big-endian and all others zero.
func h(n uint64, f byte) common.Hash {
	var x common.Hash
	x[0] = 0xb0 | f
	binary.BigEndian.PutUint64(x[24:], n)
	return x
}

// blk returns block n of chain f, linked to block n-1 of the same chain.
func blk(n uint64, f byte) chain.BlockRef {
	r := chain.BlockRef{Number: n, Hash: h(n, f), Time: 1_700_000_000 + 12*n}
	if n > 0 {
		r.Parent = h(n-1, f)
	}
	return r
}

func TestKindString(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{KindBlock, "block"},
		{KindRevert, "revert"},
		{KindFinalize, "finalize"},
		{Kind(0), "Kind(0)"},
		{Kind(9), "Kind(9)"},
		{Kind(255), "Kind(255)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.kind.String(); got != tt.want {
				t.Errorf("Kind(%d).String() = %q, want %q", uint8(tt.kind), got, tt.want)
			}
		})
	}
}

func TestIDString(t *testing.T) {
	id := ID{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}
	if got, want := id.String(), "000102030405060708090a0b0c0d0e0f"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if !(ID{}).IsZero() {
		t.Error("ID{}.IsZero() = false, want true")
	}
	for i := range len(ID{}) {
		var id ID
		id[i] = 1
		if id.IsZero() {
			t.Errorf("ID{%d: 1}.IsZero() = true, want false", i)
		}
	}
}

func TestConstants(t *testing.T) {
	if DefaultWindowCap != 100_000 {
		t.Errorf("DefaultWindowCap = %d, want 100000", DefaultWindowCap)
	}
	if MaxRecordSize != 16_777_216 {
		t.Errorf("MaxRecordSize = %d, want 16777216", MaxRecordSize)
	}
}

func TestErrorTexts(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrMalformed, "chainlog: malformed entry"},
		{ErrClosed, "chainlog: log closed"},
		{ErrTruncated, "chainlog: offset truncated"},
		{ErrAhead, "chainlog: offset beyond head"},
		{ErrBadState, "chainlog: bad state encoding"},
		{ErrIncomplete, "chainlog: incomplete record"},
		{ErrRecordTooLarge, "chainlog: record too large"},
		{ErrChecksum, "chainlog: record checksum mismatch"},
		{ErrVersion, "chainlog: unknown record version"},
		{ErrKind, "chainlog: unknown entry kind"},
		{ErrPayload, "chainlog: bad record payload"},
		{ErrTrailing, "chainlog: trailing bytes in record"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("error text = %q, want %q", got, tt.want)
			}
		})
	}
}
