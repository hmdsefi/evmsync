// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

var (
	other = common.HexToAddress("0x01")
	topA  = common.HexToHash("0xaa")
	topB  = common.HexToHash("0xbb")
	topX  = common.HexToHash("0xcc")
)

func cloneFilter(f Filter) Filter {
	c := Filter{Addresses: slices.Clone(f.Addresses)}
	if f.Topics != nil {
		c.Topics = make([][]common.Hash, len(f.Topics))
		for i, pos := range f.Topics {
			c.Topics[i] = slices.Clone(pos)
		}
	}
	return c
}

func cloneLog(l Log) Log {
	l.Topics = slices.Clone(l.Topics)
	l.Data = slices.Clone(l.Data)
	return l
}

func TestFilterMatch(t *testing.T) {
	full := Log{
		Address:     usdc,
		Topics:      []common.Hash{transfer, topA, topB},
		Data:        []byte{1, 2, 3},
		BlockNumber: 7,
		BlockHash:   one32,
		TxHash:      topX,
		TxIndex:     2,
		Index:       5,
	}
	bare := Log{Address: usdc}
	tests := []struct {
		name     string
		f        Filter
		want     bool
		wantBare bool
	}{
		{"zero filter", Filter{}, true, true},
		{"empty addresses", Filter{Addresses: []common.Address{}}, true, true},
		{"address", Filter{Addresses: []common.Address{usdc}}, true, true},
		{"other address", Filter{Addresses: []common.Address{other}}, false, false},
		{"other or address", Filter{Addresses: []common.Address{other, usdc}}, true, true},
		{"topic 0", Filter{Topics: [][]common.Hash{{transfer}}}, true, false},
		{"nil then topic 1", Filter{Topics: [][]common.Hash{nil, {topA}}}, true, false},
		{"empty then topic 1", Filter{Topics: [][]common.Hash{{}, {topA}}}, true, false},
		{"topic 0 alternatives", Filter{Topics: [][]common.Hash{{topX, transfer}}}, true, false},
		{"wrong topic 0", Filter{Topics: [][]common.Hash{{topX}}}, false, false},
		{"three wildcards", Filter{Topics: [][]common.Hash{nil, nil, nil}}, true, false},
		{"four wildcards", Filter{Topics: [][]common.Hash{nil, nil, nil, nil}}, false, false},
		{"topic in wrong position", Filter{Topics: [][]common.Hash{nil, {topB}}}, false, false},
		{
			"address and topics",
			Filter{Addresses: []common.Address{usdc}, Topics: [][]common.Hash{{transfer}, nil, {topB}}},
			true, false,
		},
		{"five wildcards", Filter{Topics: [][]common.Hash{nil, nil, nil, nil, nil}}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc, lc := cloneFilter(tt.f), cloneLog(full)
			if got := tt.f.Match(full); got != tt.want {
				t.Errorf("Match(L) = %v, want %v", got, tt.want)
			}
			if got := tt.f.Match(bare); got != tt.wantBare {
				t.Errorf("Match(log without topics) = %v, want %v", got, tt.wantBare)
			}
			allocs := testing.AllocsPerRun(100, func() { tt.f.Match(full) })
			if allocs != 0 {
				t.Errorf("Match allocates %v times per call, want 0", allocs)
			}
			if !reflect.DeepEqual(tt.f, fc) {
				t.Error("Match modified the filter")
			}
			if !reflect.DeepEqual(full, lc) {
				t.Error("Match modified the log")
			}
		})
	}
}

func TestFilterValidate(t *testing.T) {
	positions := func(n int) [][]common.Hash { return make([][]common.Hash, n) }
	tests := []struct {
		name    string
		f       Filter
		wantErr string
	}{
		{"zero filter", Filter{}, ""},
		{"4 positions", Filter{Topics: positions(4)}, ""},
		{"5 positions", Filter{Topics: positions(5)}, "chain: invalid filter: 5 topic positions, at most 4"},
		{"1000 addresses", Filter{Addresses: make([]common.Address, 1000)}, ""},
		{
			"1001 addresses",
			Filter{Addresses: make([]common.Address, 1001)},
			"chain: invalid filter: 1001 addresses, at most 1000",
		},
		{
			"1000 topics in a position",
			Filter{Topics: [][]common.Hash{make([]common.Hash, 1000)}},
			"",
		},
		{
			"positions 0 and 2 with 1001 topics",
			Filter{Topics: [][]common.Hash{make([]common.Hash, 1001), nil, make([]common.Hash, 1001)}},
			"chain: invalid filter: topic position 0 lists 1001 topics, at most 1000",
		},
		{
			"position 2 with 1001 topics",
			Filter{Topics: [][]common.Hash{nil, {topA}, make([]common.Hash, 1001)}},
			"chain: invalid filter: topic position 2 lists 1001 topics, at most 1000",
		},
		{
			"5 positions and 1001 addresses",
			Filter{Addresses: make([]common.Address, 1001), Topics: positions(5)},
			"chain: invalid filter: 5 topic positions, at most 4",
		},
		{
			"1001 addresses and 1001 topics",
			Filter{Addresses: make([]common.Address, 1001), Topics: [][]common.Hash{make([]common.Hash, 1001)}},
			"chain: invalid filter: 1001 addresses, at most 1000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.f.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want %q", tt.wantErr)
			}
			if !errors.Is(err, ErrInvalidFilter) {
				t.Errorf("Validate() = %v, want an error wrapping ErrInvalidFilter", err)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("Validate() = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}
