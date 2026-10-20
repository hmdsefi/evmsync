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
