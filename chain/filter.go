// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"fmt"
	"slices"

	"github.com/ethereum/go-ethereum/common"
)

// MaxTopicPositions is the number of topic positions a filter may constrain (LOG0 to LOG4 give
// logs at most four topics).
const MaxTopicPositions = 4

// MaxFilterValues is the largest number of addresses, and of topics in one position, a filter may
// list. It is go-ethereum's default LogQueryLimit.
const MaxFilterValues = 1000

// Filter selects logs with eth_getLogs semantics. A nil or empty Addresses matches every address.
// Topics[i] constrains topic i: a nil or empty Topics[i] matches any topic, otherwise the log's
// topic i must equal one of the listed hashes. A log with fewer topics than len(Topics) never
// matches, even when the extra positions are wildcards. Match and Validate only read f.
type Filter struct {
	Addresses []common.Address
	Topics    [][]common.Hash
}

// Match reports whether l passes f. It reads only l.Address and l.Topics.
func (f Filter) Match(l Log) bool {
	if len(f.Addresses) > 0 && !slices.Contains(f.Addresses, l.Address) {
		return false
	}
	if len(f.Topics) > len(l.Topics) {
		return false
	}
	for i, pos := range f.Topics {
		if len(pos) > 0 && !slices.Contains(pos, l.Topics[i]) {
			return false
		}
	}
	return true
}

// Validate returns an error wrapping ErrInvalidFilter when f has more than MaxTopicPositions
// topic positions, more than MaxFilterValues addresses, or more than MaxFilterValues topics in one
// position. Duplicates are allowed.
func (f Filter) Validate() error {
	if len(f.Topics) > MaxTopicPositions {
		return fmt.Errorf("%w: %d topic positions, at most %d", ErrInvalidFilter, len(f.Topics), MaxTopicPositions)
	}
	if len(f.Addresses) > MaxFilterValues {
		return fmt.Errorf("%w: %d addresses, at most %d", ErrInvalidFilter, len(f.Addresses), MaxFilterValues)
	}
	for i, pos := range f.Topics {
		if len(pos) > MaxFilterValues {
			return fmt.Errorf("%w: topic position %d lists %d topics, at most %d",
				ErrInvalidFilter, i, len(pos), MaxFilterValues)
		}
	}
	return nil
}
