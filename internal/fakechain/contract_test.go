// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

// contractChain builds the chain.Source contract fixture: blocks 0..10 canonical, a side block s7
// known by hash only, finalized block 5.
func contractChain(seed uint64) (c *Chain, s7 chain.Header) {
	c = New(seed, Params{})
	c.Mine(7)
	s7 = c.Head()
	c.Reorg(1, 1)
	c.Mine(3)
	c.SetFinalized(5)
	return c, s7
}

// contractSeed returns the first seed from 1 whose canonical block 7 has at least two logs with
// pairwise different (Address, Topics) and whose block 8 has none.
func contractSeed(t *testing.T) uint64 {
	t.Helper()
	for seed := uint64(1); seed <= 1000; seed++ {
		c, _ := contractChain(seed)
		h7, _ := c.Canonical(7)
		h8, _ := c.Canonical(8)
		b7, _ := c.Block(h7.Ref.Hash, chain.Filter{})
		b8, _ := c.Block(h8.Ref.Hash, chain.Filter{})
		if len(b7.Logs) >= 2 && len(b8.Logs) == 0 && distinctLogs(b7.Logs) {
			return seed
		}
	}
	t.Fatal("no seed in 1..1000 gives the contract fixture")
	return 0
}

func distinctLogs(logs []chain.Log) bool {
	for i := range logs {
		for j := i + 1; j < len(logs); j++ {
			if logs[i].Address == logs[j].Address && slices.Equal(logs[i].Topics, logs[j].Topics) {
				return false
			}
		}
	}
	return true
}

func TestSourceContract(t *testing.T) {
	seed := contractSeed(t)
	c, s7 := contractChain(seed)
	src := NewSource(c, seed, Faults{})
	ctx := t.Context()
	h := func(n uint64) chain.Header {
		hd, _ := c.Canonical(n)
		return hd
	}
	random := common.HexToHash("0xdeadbeef")
	b7, _ := c.Block(h(7).Ref.Hash, chain.Filter{})
	last := b7.Logs[len(b7.Logs)-1]
	lastFilter := chain.Filter{Addresses: []common.Address{last.Address}}
	for _, tp := range last.Topics {
		lastFilter.Topics = append(lastFilter.Topics, []common.Hash{tp})
	}

	header := func(want chain.Header, call func() (chain.Header, error)) func(t *testing.T) {
		return func(t *testing.T) {
			got, err := call()
			if err != nil || got != want {
				t.Errorf("got %v, %v, want %v", got.Ref, err, want.Ref)
			}
		}
	}
	fails := func(target error, call func() error) func(t *testing.T) {
		return func(t *testing.T) {
			if err := call(); !errors.Is(err, target) {
				t.Errorf("error %v, want one wrapping %v", err, target)
			}
		}
	}
	logs := func(want []chain.Log, call func() ([]chain.Log, error)) func(t *testing.T) {
		return func(t *testing.T) {
			got, err := call()
			if err != nil || !logsEqual(got, want) || (got == nil) != (want == nil) {
				t.Errorf("got %v, %v, want %v", got, err, want)
			}
			checkLogValues(t, c, got)
		}
	}

	var all []chain.Log
	for _, b := range c.CanonicalBlocks(chain.Filter{}) {
		all = append(all, b.Logs...)
	}
	unsupported := NewSource(c, seed, Faults{FinalizedUnsupported: true})

	cases := []struct {
		name  string
		check func(t *testing.T)
	}{
		{"Latest", header(h(10), func() (chain.Header, error) { return src.Latest(ctx) })},
		{"Finalized", header(h(5), func() (chain.Header, error) { return src.Finalized(ctx) })},
		{"Finalized unsupported", fails(chain.ErrUnsupported, func() error {
			_, err := unsupported.Finalized(ctx)
			return err
		})},
		{"HeaderByNumber(0)", header(c.Genesis(), func() (chain.Header, error) { return src.HeaderByNumber(ctx, 0) })},
		{"HeaderByNumber(10)", header(h(10), func() (chain.Header, error) { return src.HeaderByNumber(ctx, 10) })},
		{"HeaderByNumber(11)", fails(chain.ErrNotFound, func() error {
			_, err := src.HeaderByNumber(ctx, 11)
			return err
		})},
		{"HeaderByHash(7)", header(h(7), func() (chain.Header, error) { return src.HeaderByHash(ctx, h(7).Ref.Hash) })},
		{"HeaderByHash(s7)", header(s7, func() (chain.Header, error) { return src.HeaderByHash(ctx, s7.Ref.Hash) })},
		{"HeaderByHash(random)", fails(chain.ErrNotFound, func() error {
			_, err := src.HeaderByHash(ctx, random)
			return err
		})},
		{"HeaderByHash(zero)", fails(chain.ErrNotFound, func() error {
			_, err := src.HeaderByHash(ctx, common.Hash{})
			return err
		})},
		{"Logs(7)", logs(b7.Logs, func() ([]chain.Log, error) { return src.Logs(ctx, h(7).Ref.Hash, chain.Filter{}) })},
		{"Logs(8)", logs(nil, func() ([]chain.Log, error) { return src.Logs(ctx, h(8).Ref.Hash, chain.Filter{}) })},
		{"Logs(random)", fails(chain.ErrNotFound, func() error {
			_, err := src.Logs(ctx, random, chain.Filter{})
			return err
		})},
		{"Logs(7, last log's filter)", logs([]chain.Log{last}, func() ([]chain.Log, error) {
			return src.Logs(ctx, h(7).Ref.Hash, lastFilter)
		})},
		{"LogsInRange(0, 10)", logs(all, func() ([]chain.Log, error) { return src.LogsInRange(ctx, 0, 10, chain.Filter{}) })},
		{"LogsInRange(8, 8)", logs(nil, func() ([]chain.Log, error) { return src.LogsInRange(ctx, 8, 8, chain.Filter{}) })},
		{"LogsInRange(5, 4)", fails(chain.ErrInvalidRange, func() error {
			_, err := src.LogsInRange(ctx, 5, 4, chain.Filter{})
			return err
		})},
		{"LogsInRange(5, 11)", fails(chain.ErrNotFound, func() error {
			_, err := src.LogsInRange(ctx, 5, 11, chain.Filter{})
			return err
		})},
		{"cancelled context", func(t *testing.T) { checkCancelled(t, src, h(7).Ref.Hash) }},
		{"results belong to the caller", func(t *testing.T) {
			first, _ := src.Logs(ctx, h(7).Ref.Hash, chain.Filter{})
			first[0].Topics[0] = common.Hash{1}
			again, _ := src.Logs(ctx, h(7).Ref.Hash, chain.Filter{})
			if !logsEqual(again, b7.Logs) {
				t.Error("mutating Topics[0] of a Logs result changed a later result")
			}
			rng, _ := src.LogsInRange(ctx, 0, 10, chain.Filter{})
			rng[0].Topics[0] = common.Hash{1}
			rng2, _ := src.LogsInRange(ctx, 0, 10, chain.Filter{})
			if !logsEqual(rng2, all) {
				t.Error("mutating Topics[0] of a LogsInRange result changed a later result")
			}
		}},
		{"filter is read-only", func(t *testing.T) {
			f := chain.Filter{Addresses: []common.Address{Address(0), Address(1)},
				Topics: [][]common.Hash{{TransferTopic, ApprovalTopic}, nil}}
			keep := chain.Filter{Addresses: slices.Clone(f.Addresses),
				Topics: [][]common.Hash{slices.Clone(f.Topics[0]), nil}}
			_, _ = src.Logs(ctx, h(7).Ref.Hash, f)
			_, _ = src.LogsInRange(ctx, 0, 10, f)
			if !slices.Equal(f.Addresses, keep.Addresses) || !slices.Equal(f.Topics[0], keep.Topics[0]) ||
				len(f.Topics) != 2 || f.Topics[1] != nil {
				t.Error("a Source method changed its filter")
			}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, tt.check)
	}
}

// checkLogValues checks the chain.Log rules for a result: each log names its block, the empty
// fields are nil, and each block's logs have strictly increasing Index and non-decreasing TxIndex.
func checkLogValues(t *testing.T, c *Chain, logs []chain.Log) {
	t.Helper()
	for i, l := range logs {
		hd, ok := c.Header(l.BlockHash)
		if !ok || hd.Ref.Number != l.BlockNumber {
			t.Errorf("log %d names block %d %s, which is not a tree block", i, l.BlockNumber, l.BlockHash.Hex())
		}
		if len(l.Topics) > 4 || (l.Topics != nil && len(l.Topics) == 0) || (l.Data != nil && len(l.Data) == 0) {
			t.Errorf("log %d breaks the empty-is-nil or topic-count rule: %+v", i, l)
		}
		if i == 0 {
			continue
		}
		p := logs[i-1]
		switch {
		case p.BlockNumber > l.BlockNumber:
			t.Errorf("log %d: block %d after block %d", i, l.BlockNumber, p.BlockNumber)
		case p.BlockNumber == l.BlockNumber && (p.Index >= l.Index || p.TxIndex > l.TxIndex):
			t.Errorf("log %d of block %d: Index %d TxIndex %d after Index %d TxIndex %d",
				i, l.BlockNumber, l.Index, l.TxIndex, p.Index, p.TxIndex)
		}
	}
}

func checkCancelled(t *testing.T, src *Source, h common.Hash) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	errs := []error{}
	_, err := src.Latest(ctx)
	errs = append(errs, err)
	_, err = src.Finalized(ctx)
	errs = append(errs, err)
	_, err = src.HeaderByNumber(ctx, 99)
	errs = append(errs, err)
	_, err = src.HeaderByHash(ctx, h)
	errs = append(errs, err)
	_, err = src.Logs(ctx, common.Hash{}, chain.Filter{})
	errs = append(errs, err)
	_, err = src.LogsInRange(ctx, 5, 4, chain.Filter{})
	errs = append(errs, err)
	for i, err := range errs {
		if !errors.Is(err, context.Canceled) || errors.Is(err, chain.ErrNotFound) ||
			errors.Is(err, chain.ErrUnsupported) || errors.Is(err, chain.ErrInvalidRange) {
			t.Errorf("call %d with a cancelled context: error %v", i, err)
		}
	}
}
