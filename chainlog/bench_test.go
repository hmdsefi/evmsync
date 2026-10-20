// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"testing"

	"github.com/hmdsefi/evmsync/chain"
)

// benchBlock is a block with 10 logs of 3 topics and 64 bytes of data each.
func benchBlock() Entry {
	ref := blk(7, 0)
	return Entry{Offset: 1 << 20, Kind: KindBlock, Block: chain.Block{Ref: ref, Logs: mkLogs(ref, 10, 3, 64)}}
}

func BenchmarkEncode(b *testing.B) {
	e := benchBlock()
	dst := make([]byte, 0, 4096)
	b.ReportAllocs()
	for b.Loop() {
		dst = Encode(dst[:0], e)
	}
}

func BenchmarkDecode(b *testing.B) {
	rec := Encode(nil, benchBlock())
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := Decode(rec); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStateBlock checks and applies one linked block and a Finalize of the window's first
// ref per iteration, so the window keeps 99,999 refs.
func BenchmarkStateBlock(b *testing.B) {
	const window = 99_999
	s := NewState(0)
	s.Apply(finalize(blk(0, 0)))
	for n := uint64(1); n <= window; n++ {
		s.Apply(block(blk(n, 0)))
	}
	next := uint64(window + 1)
	b.ReportAllocs()
	for b.Loop() {
		e := block(blk(next, 0))
		f := finalize(blk(next-window, 0))
		if err := s.Check(e); err != nil {
			b.Fatal(err)
		}
		s.Apply(e)
		if err := s.Check(f); err != nil {
			b.Fatal(err)
		}
		s.Apply(f)
		next++
	}
}
