// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/consumer"
)

// nop is a consumer whose Apply only commits the offset.
type nop struct{ off chainlog.Offset }

func (n *nop) Offset(context.Context) (chainlog.Offset, error) { return n.off, nil }

func (n *nop) Apply(_ context.Context, e chainlog.Entry) error {
	n.off = e.Offset
	return nil
}

// BenchmarkRun runs a no-op consumer over a log of 100,000 block entries per iteration and
// reports the time per entry.
func BenchmarkRun(b *testing.B) {
	const entries = 100_000
	c, rs := refs(b, entries)
	es := chainEntries(c, rs, entries+1)[1:] // blocks only: blk(A1) … blk(A100000)
	log := newLog(b, es...)
	b.ReportAllocs()
	for b.Loop() {
		ctx, cancel := context.WithCancel(b.Context())
		if err := consumer.Run(ctx, log, &canceler{Consumer: &nop{}, at: entries, cancel: cancel}); err != context.Canceled {
			b.Fatalf("Run = %v, want context.Canceled", err)
		}
		cancel()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/entries, "ns/entry")
}

// BenchmarkMemoryApply applies one block on top of pending blocks per iteration, with apply and
// clone functions that do nothing. Every 1,024 blocks a Finalize leaves only the newest block
// pending, so the pending blocks stay bounded.
func BenchmarkMemoryApply(b *testing.B) {
	ctx := b.Context()
	m := consumer.NewMemory(struct{}{},
		func(s struct{}, _ chain.Block) struct{} { return s },
		func(s struct{}) struct{} { return s })
	ref := func(n uint64) chain.BlockRef {
		var h common.Hash
		binary.BigEndian.PutUint64(h[24:], n+1)
		return chain.BlockRef{Number: n, Hash: h}
	}
	off := chainlog.Offset(0)
	next := func(e chainlog.Entry) {
		off++
		e.Offset = off
		if err := m.Apply(ctx, e); err != nil {
			b.Fatal(err)
		}
	}
	next(chainlog.Entry{Kind: chainlog.KindFinalize, To: ref(0)})
	n := uint64(0)
	b.ReportAllocs()
	for b.Loop() {
		n++
		next(chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: ref(n)}})
		if n%1024 == 0 {
			next(chainlog.Entry{Kind: chainlog.KindFinalize, To: ref(n - 1)})
		}
	}
}
