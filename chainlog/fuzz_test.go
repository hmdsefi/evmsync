// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

func FuzzDecode(f *testing.F) {
	ref := blk(7, 0)
	three := Encode(nil, Entry{Offset: 3, Kind: KindBlock, Block: chain.Block{Ref: ref, Logs: mkLogs(ref, 3, 2, 5)}})
	flipped := bytes.Clone(three)
	flipped[len(flipped)-10] ^= 0x01
	tooLarge := make([]byte, 8)
	binary.LittleEndian.PutUint32(tooLarge, MaxRecordSize+1)

	f.Add(Encode(nil, Entry{Offset: 1, Kind: KindBlock, Block: chain.Block{Ref: ref}}))
	f.Add(Encode(nil, revertR()))
	f.Add(Encode(nil, Entry{Offset: 2, Kind: KindFinalize, To: ref}))
	f.Add(three)
	f.Add(three[:len(three)-1])
	f.Add(flipped)
	f.Add(make([]byte, 8))
	f.Add(tooLarge)
	f.Add(record(cat(Encode(nil, revertR())[8:], []byte{0x00})))

	f.Fuzz(func(t *testing.T, data []byte) {
		e, n, err := Decode(data)
		if err != nil {
			matched := 0
			for _, ce := range codecErrors {
				if errors.Is(err, ce) {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("error %q wraps %d codec errors, want 1", err, matched)
			}
			if n != 0 || !reflect.DeepEqual(e, Entry{}) {
				t.Fatalf("failed Decode returned %+v, %d; want a zero entry and 0", e, n)
			}
			return
		}
		length := int(binary.LittleEndian.Uint32(data))
		if n != 8+length || n > len(data) {
			t.Fatalf("n = %d, want 8+%d and at most %d", n, length, len(data))
		}
		if again := Encode(nil, e); !bytes.Equal(again, data[:n]) {
			t.Fatalf("Encode(Decode(data)) =\n%x\nwant\n%x", again, data[:n])
		}
	})
}

func FuzzRoundTrip(f *testing.F) {
	h1, h2 := h(7, 0), h(6, 0)
	// The AT-LOG-19 entries: offsets 1, 127, 128 and the largest; numbers 0 and the largest; 0, 1
	// and 7 logs; 0 to 4 topics; 0, 1 and 70,000 bytes of data; the largest tx index and index;
	// Final both ways; revert and finalize.
	f.Add(uint8(1), uint64(1), uint64(7), h1[:], h2[:], uint64(1000), false, uint8(0), uint8(0), []byte(nil), uint32(0), uint32(0))
	f.Add(uint8(1), uint64(127), uint64(0), h1[:], h2[:], uint64(0), false, uint8(0), uint8(0), []byte(nil), uint32(0), uint32(0))
	f.Add(uint8(2), uint64(128), uint64(math.MaxUint64), h1[:], h2[:], uint64(math.MaxUint64), false, uint8(0), uint8(0), []byte(nil), uint32(0), uint32(0))
	f.Add(uint8(0), uint64(math.MaxUint64), uint64(7), h1[:], h2[:], uint64(1000), true, uint8(1), uint8(4), []byte{1}, uint32(math.MaxUint32), uint32(math.MaxUint32))
	f.Add(uint8(0), uint64(5), uint64(7), h1[:], h2[:], uint64(1000), false, uint8(7), uint8(3), make([]byte, 70_000), uint32(2), uint32(10))
	f.Add(uint8(0), uint64(5), uint64(math.MaxUint64), h1[:], h2[:], uint64(1000), true, uint8(1), uint8(1), []byte{}, uint32(0), uint32(0))
	f.Add(uint8(0), uint64(5), uint64(0), []byte{}, []byte{}, uint64(0), false, uint8(2), uint8(2), []byte{9, 9}, uint32(1), uint32(1))

	f.Fuzz(func(t *testing.T, kind uint8, offset, number uint64, hash, parent []byte, time uint64,
		final bool, nlogs, ntopics uint8, data []byte, txindex, index uint32,
	) {
		ref := chain.BlockRef{Number: number, Time: time}
		copy(ref.Hash[:], hash)
		ref.Hash[0] = 1
		copy(ref.Parent[:], parent)
		e := Entry{Offset: Offset(max(offset, 1)), Kind: Kind(1 + kind%3)}
		if e.Kind != KindBlock {
			e.To = ref
		} else {
			e.Final = final
			e.Block.Ref = ref
			for i := range uint32(nlogs % 8) {
				if index > math.MaxUint32-i {
					t.Skip("index + i overflows uint32")
				}
				l := chain.Log{
					Address: common.Address(ref.Parent[:20]),
					Data:    data,
					TxHash:  ref.Hash,
					TxIndex: txindex,
					Index:   index + i,
				}
				for j := range ntopics % 5 {
					l.Topics = append(l.Topics, common.Hash{0: j, 31: byte(i)})
				}
				e.Block.Logs = append(e.Block.Logs, l)
			}
		}
		rec := Encode(nil, e)
		got, n, err := Decode(rec)
		if err != nil {
			t.Fatalf("Decode(Encode(e)): %v", err)
		}
		if n != len(rec) {
			t.Fatalf("n = %d, want %d", n, len(rec))
		}
		if want := normalize(e); !reflect.DeepEqual(got, want) {
			t.Fatalf("Decode(Encode(e)) =\n%+v\nwant\n%+v", got, want)
		}
	})
}
