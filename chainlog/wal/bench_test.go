// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"testing"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// benchBlock returns block n of chain 0 with 10 logs of 3 topics and 64 bytes of data each.
func benchBlock(n uint64) chainlog.Entry {
	r := ref(n)
	logs := make([]chain.Log, 10)
	for i := range logs {
		l := chain.Log{BlockNumber: r.Number, BlockHash: r.Hash, TxHash: r.Hash, TxIndex: uint32(i), Index: uint32(i)} //nolint:gosec // i < 10
		l.Address[19] = 0xaa
		l.Topics = append(l.Topics, r.Hash, r.Parent, r.Hash)
		l.Data = make([]byte, 64)
		logs[i] = l
	}
	return chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r, Logs: logs}}
}

// fill appends blocks 1..n to l, finalizing every 1,024 blocks so the window stays small.
func fill(b *testing.B, l *Log, n uint64) {
	b.Helper()
	ctx := b.Context()
	for num := uint64(1); num <= n; num++ {
		es := []chainlog.Entry{benchBlock(num)}
		if num%1024 == 0 {
			es = append(es, chainlog.Entry{Kind: chainlog.KindFinalize, To: ref(num)})
		}
		if _, err := l.Append(ctx, es...); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAppend(b *testing.B) {
	b.Run("never", func(b *testing.B) { benchAppend(b, SyncNever) })
	b.Run("always", func(b *testing.B) { benchAppend(b, SyncAlways) })
}

// benchAppend appends one block per iteration, and a Finalize every 1,024 blocks.
func benchAppend(b *testing.B, sync SyncPolicy) {
	ctx := b.Context()
	l, err := Open(b.TempDir(), Options{Sync: sync})
	if err != nil {
		b.Fatal(err)
	}
	defer l.Close()
	next := uint64(1)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := l.Append(ctx, benchBlock(next)); err != nil {
			b.Fatal(err)
		}
		if next%1024 == 0 {
			if _, err := l.Append(ctx, chainlog.Entry{Kind: chainlog.KindFinalize, To: ref(next)}); err != nil {
				b.Fatal(err)
			}
		}
		next++
	}
}

// BenchmarkRead reads 256 entries per call, sequentially through a log of 65,536 blocks.
func BenchmarkRead(b *testing.B) {
	const n, batch = 1 << 16, 256
	ctx := b.Context()
	l, err := Open(b.TempDir(), Options{Sync: SyncNever})
	if err != nil {
		b.Fatal(err)
	}
	defer l.Close()
	fill(b, l, n)
	head, _ := l.Head(ctx)
	from := chainlog.Offset(1)
	b.ReportAllocs()
	for b.Loop() {
		es, err := l.Read(ctx, from, batch)
		if err != nil {
			b.Fatal(err)
		}
		from += chainlog.Offset(len(es)) //nolint:gosec // at most 256
		if from > head {
			from = 1
		}
	}
}

// BenchmarkOpen opens a 256 MiB log with no checkpoint, so Open scans and replays all of it.
func BenchmarkOpen(b *testing.B) {
	const size = 256 << 20
	dir := b.TempDir()
	opts := Options{Sync: SyncNever, SegmentSize: 512 << 20}
	l, err := Open(dir, opts)
	if err != nil {
		b.Fatal(err)
	}
	perBlock := uint64(len(chainlog.Encode(nil, chainlog.Entry{Offset: 1 << 20, Kind: chainlog.KindBlock, Block: benchBlock(1).Block})))
	fill(b, l, size/perBlock)
	if err := l.Close(); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(size)
	b.ReportAllocs()
	for b.Loop() {
		l, err := Open(dir, opts)
		if err != nil {
			b.Fatal(err)
		}
		if err := l.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
