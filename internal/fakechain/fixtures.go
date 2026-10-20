// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

// Address returns contract address i (0 <= i < 4): byte 0 is 0x10, byte 19 is i, the rest zero.
// It panics outside that range.
func Address(i int) common.Address {
	return fixture(0x10, i, 4)
}

// Account returns account address j (0 <= j < 8): byte 0 is 0x20, byte 19 is j, the rest zero. It
// panics outside that range.
func Account(j int) common.Address {
	return fixture(0x20, j, 8)
}

func fixture(prefix byte, i, n int) common.Address {
	if i < 0 || i >= n {
		panic(fmt.Sprintf("evmsync: fakechain: fixture index %d out of range", i))
	}
	var a common.Address
	a[0], a[19] = prefix, byte(i) //nolint:gosec // i < n <= 8, checked above
	return a
}

var (
	// TransferTopic is keccak256("Transfer(address,address,uint256)").
	TransferTopic = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	// ApprovalTopic is keccak256("Approval(address,address,uint256)").
	ApprovalTopic = common.HexToHash("0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925")
	// OtherTopic is keccak256("Other(uint256)").
	OtherTopic = common.HexToHash("0x42d43c8775dfae639a33729abbd197262a51e5f0a31afc5914fbf109e1fd527f")
)

// DefaultFilter selects Transfer and Approval logs of contracts 0 and 1, so contracts 2 and 3 and
// Other logs are filtered out.
var DefaultFilter = chain.Filter{
	Addresses: []common.Address{Address(0), Address(1)},
	Topics:    [][]common.Hash{{TransferTopic, ApprovalTopic}},
}

// GenesisTime is the Time of block 0, in unix seconds; block n has Time GenesisTime + 12*n.
const GenesisTime = 1_700_000_000
