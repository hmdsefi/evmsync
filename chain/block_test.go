// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

var (
	usdc     = common.HexToAddress("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174")
	transfer = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	one32    = common.HexToHash("0x01")
	zero32   = common.Hash{}
)

func TestBlockRefString(t *testing.T) {
	tests := []struct {
		name string
		ref  BlockRef
		want string
	}{
		{
			name: "number and hash",
			ref:  BlockRef{Number: 123, Hash: transfer},
			want: "123 0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
		},
		{
			name: "zero ref",
			ref:  BlockRef{},
			want: "0 0x" + strings.Repeat("0", 64),
		},
		{
			name: "parent and time not printed",
			ref:  BlockRef{Number: 123, Hash: transfer, Parent: one32, Time: 1_700_000_000},
			want: "123 0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComparable(t *testing.T) {
	h, p := transfer, one32
	base := BlockRef{1, h, p, 5}
	if base != (BlockRef{1, h, p, 5}) {
		t.Fatal("equal refs compare unequal")
	}
	changed := []BlockRef{
		{2, h, p, 5},
		{1, p, p, 5},
		{1, h, h, 5},
		{1, h, p, 6},
	}
	for i, r := range changed {
		if base == r {
			t.Errorf("ref with field %d changed compares equal", i)
		}
	}
	var h1, h2 Header
	if h1 != h2 {
		t.Error("zero headers compare unequal")
	}
	h2.Bloom[0] = 1
	if h1 == h2 {
		t.Error("headers with different blooms compare equal")
	}
	if h1.Bloom == h2.Bloom {
		t.Error("different blooms compare equal")
	}
}
