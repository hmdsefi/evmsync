// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

var codecErrors = []error{
	ErrIncomplete, ErrRecordTooLarge, ErrChecksum, ErrVersion, ErrKind, ErrPayload, ErrTrailing,
}

// record returns the 8-byte header for body (length, CRC-32C) followed by body.
func record(body []byte) []byte {
	r := binary.LittleEndian.AppendUint32(nil, uint32(len(body))) //nolint:gosec // test bodies are small
	r = binary.LittleEndian.AppendUint32(r, crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli)))
	return append(r, body...)
}

func refBytes(r chain.BlockRef) []byte {
	b := binary.AppendUvarint(nil, r.Number)
	b = append(b, r.Hash[:]...)
	b = append(b, r.Parent[:]...)
	return binary.AppendUvarint(b, r.Time)
}

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func rep(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }

func uv(x uint64) []byte { return binary.AppendUvarint(nil, x) }

func panicText(f func()) (s string) {
	defer func() { s = fmt.Sprint(recover()) }()
	f()
	return ""
}

// mkLogs returns n logs of block ref, each with ntopics topics and dataLen bytes of data. Each log
// has its own Topics and Data arrays.
func mkLogs(ref chain.BlockRef, n, ntopics, dataLen int) []chain.Log {
	logs := make([]chain.Log, n)
	for i := range logs {
		l := chain.Log{
			Address:     common.Address{0: byte(i), 19: 0xaa},
			BlockNumber: ref.Number,
			BlockHash:   ref.Hash,
			TxHash:      common.Hash{0: 0x77, 31: byte(i)},
			TxIndex:     uint32(i / 2),
			Index:       uint32(i),
		}
		for j := range ntopics {
			l.Topics = append(l.Topics, common.Hash{0: byte(j + 1), 31: byte(i)})
		}
		if dataLen > 0 {
			l.Data = rep(byte(i+1), dataLen)
		}
		logs[i] = l
	}
	return logs
}

// normalize returns e as Decode returns it: the fields of its kind only, the logs' block fields
// set from the ref and empty slices nil.
func normalize(e Entry) Entry {
	if e.Kind != KindBlock {
		return Entry{Offset: e.Offset, Kind: e.Kind, To: e.To}
	}
	out := Entry{Offset: e.Offset, Kind: e.Kind, Final: e.Final, Block: chain.Block{Ref: e.Block.Ref}}
	for _, l := range e.Block.Logs {
		l.BlockNumber = e.Block.Ref.Number
		l.BlockHash = e.Block.Ref.Hash
		if len(l.Topics) == 0 {
			l.Topics = nil
		}
		if len(l.Data) == 0 {
			l.Data = nil
		}
		out.Block.Logs = append(out.Block.Logs, l)
	}
	return out
}

var (
	hash11 = common.Hash(rep(0x11, 32))
	hash22 = common.Hash(rep(0x22, 32))
)

// revertR is the revert of the layout test; its record is 78 bytes.
func revertR() Entry {
	return Entry{Offset: 1, Kind: KindRevert, To: chain.BlockRef{Number: 5, Hash: hash11, Parent: hash22, Time: 1000}}
}

// finalBlock is the block of the layout test: Final, one log with address 20×aa, one topic, data 01 02,
// tx hash 32×bb, tx index 1, index 300.
func finalBlock() (Entry, common.Hash) {
	ref := blk(7, 0)
	topic := common.Hash{31: 1}
	return Entry{Offset: 200, Kind: KindBlock, Final: true, Block: chain.Block{Ref: ref, Logs: []chain.Log{{
		Address:     common.Address(rep(0xaa, 20)),
		Topics:      []common.Hash{topic},
		Data:        []byte{1, 2},
		BlockNumber: ref.Number,
		BlockHash:   ref.Hash,
		TxHash:      common.Hash(rep(0xbb, 32)),
		TxIndex:     1,
		Index:       300,
	}}}}, topic
}

func TestEncodeLayout(t *testing.T) {
	revertBody := cat([]byte{0x01, 0x02, 0x01, 0x05}, rep(0x11, 32), rep(0x22, 32), []byte{0xe8, 0x07})
	fb, topic := finalBlock()
	ref := fb.Block.Ref
	plain := Entry{Offset: 128, Kind: KindBlock, Block: chain.Block{Ref: ref}}
	fin := Entry{Offset: 3, Kind: KindFinalize, To: ref}
	// A revert with block fields set writes only To.
	noisyRevert := revertR()
	noisyRevert.Final = true
	noisyRevert.Block = chain.Block{Ref: ref, Logs: fb.Block.Logs}

	tests := []struct {
		name string
		e    Entry
		body []byte
	}{
		{"revert", revertR(), revertBody},
		{"revert ignores block fields", noisyRevert, revertBody},
		{"final block with one log", fb, cat(
			[]byte{0x01, 0x01, 0xc8, 0x01}, refBytes(ref), []byte{0x01, 0x01},
			rep(0xaa, 20), []byte{0x01}, topic[:], []byte{0x02, 0x01, 0x02},
			rep(0xbb, 32), []byte{0x01, 0xac, 0x02},
		)},
		{"block without logs", plain, cat([]byte{0x01, 0x01, 0x80, 0x01}, refBytes(ref), []byte{0x00, 0x00})},
		{"finalize", fin, cat([]byte{0x01, 0x03, 0x03}, refBytes(ref))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Encode(nil, tt.e)
			if want := record(tt.body); !bytes.Equal(got, want) {
				t.Errorf("Encode =\n%x\nwant\n%x", got, want)
			}
		})
	}

	r := Encode(nil, revertR())
	if len(r) != 78 || !bytes.Equal(r[:4], []byte{0x46, 0x00, 0x00, 0x00}) {
		t.Errorf("revert record: %d bytes, header %x; want 78 bytes, header 46000000", len(r), r[:4])
	}
	if got, want := binary.LittleEndian.Uint32(r[4:8]), crc32.Checksum(r[8:], crc32.MakeTable(crc32.Castagnoli)); got != want {
		t.Errorf("revert record CRC = %#08x, want %#08x", got, want)
	}

	prefix := []byte("xyz")
	got := Encode(prefix, revertR())
	if want := cat([]byte("xyz"), r); !bytes.Equal(got, want) {
		t.Errorf("Encode with a prefix =\n%x\nwant\n%x", got, want)
	}
}

func TestEncodePanics(t *testing.T) {
	five := mkLogs(blk(7, 0), 2, 4, 0)
	five[1].Topics = append(five[1].Topics, common.Hash{1})
	tests := []struct {
		name string
		e    Entry
		want string
	}{
		{"kind 0", Entry{Offset: 1, To: blk(1, 0)}, "evmsync: chainlog.Encode: unknown kind 0"},
		{"kind 4", Entry{Offset: 1, Kind: 4, To: blk(1, 0)}, "evmsync: chainlog.Encode: unknown kind 4"},
		{"kind 0 and offset 0", Entry{}, "evmsync: chainlog.Encode: unknown kind 0"},
		{"offset 0", Entry{Kind: KindBlock, Block: chain.Block{Ref: blk(7, 0)}}, "evmsync: chainlog.Encode: offset 0"},
		{"offset 0 revert", Entry{Kind: KindRevert, To: blk(7, 0)}, "evmsync: chainlog.Encode: offset 0"},
		{"5 topics", Entry{Offset: 1, Kind: KindBlock, Block: chain.Block{Ref: blk(7, 0), Logs: five}}, "evmsync: chainlog.Encode: log 1 has 5 topics"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panicText(func() { Encode(nil, tt.e) }); got != tt.want {
				t.Errorf("panic = %q, want %q", got, tt.want)
			}
		})
	}

	// A revert's unused block fields are not checked.
	r := Entry{Offset: 1, Kind: KindRevert, To: blk(7, 0), Block: chain.Block{Logs: five}}
	if got := panicText(func() { Encode(nil, r) }); got != "<nil>" {
		t.Errorf("Encode of a revert with 5-topic block logs panicked: %s", got)
	}

	e := Entry{Offset: 1 << 40, Kind: KindBlock, Block: chain.Block{Ref: blk(7, 0), Logs: mkLogs(blk(7, 0), 10, 3, 64)}}
	dst := make([]byte, 0, 4096)
	if n := testing.AllocsPerRun(100, func() { dst = Encode(dst[:0], e) }); n != 0 {
		t.Errorf("Encode into a large enough dst: %v allocs, want 0", n)
	}
}

func TestDecodeErrors(t *testing.T) {
	r := Encode(nil, revertR())
	rb := r[8:] // the 70-byte revert body
	afterOffset := rb[3:]
	ref := blk(7, 0)
	blockHead := cat([]byte{0x01, 0x01, 0x01}, refBytes(ref))

	withLen := func(n uint32) []byte {
		c := bytes.Clone(r)
		binary.LittleEndian.PutUint32(c, n)
		return c
	}
	flipped := bytes.Clone(r)
	flipped[8+10] ^= 0xff
	stored := binary.LittleEndian.Uint32(flipped[4:8])
	computed := crc32.Checksum(flipped[8:], crc32.MakeTable(crc32.Castagnoli))
	setByte := func(i int, v byte) []byte {
		b := bytes.Clone(rb)
		b[i] = v
		return record(b)
	}
	nlogs1000 := cat(blockHead, []byte{0x00}, uv(1000))
	nlogs1000Body := cat(nlogs1000, make([]byte, 200-len(nlogs1000)))
	minLog := cat(rep(0xaa, 20), []byte{0x00, 0x00}, rep(0xbb, 32), []byte{0x00})
	fb, _ := finalBlock()
	fbBody := Encode(nil, fb)[8:]

	tests := []struct {
		name   string
		src    []byte
		err    error
		detail string
	}{
		{"5 bytes", r[:5], ErrIncomplete, "have 5 bytes, need 8"},
		{"empty", nil, ErrIncomplete, "have 0 bytes, need 8"},
		{"length 16777217", withLen(16777217), ErrRecordTooLarge, "body of 16777217 bytes exceeds 16777216"},
		{"length max uint32", withLen(math.MaxUint32), ErrRecordTooLarge, "body of 4294967295 bytes exceeds 16777216"},
		{"77 bytes", r[:77], ErrIncomplete, "have 77 bytes, need 78"},
		{"length 100", withLen(100), ErrIncomplete, "have 78 bytes, need 108"},
		{"body byte 10 flipped", flipped, ErrChecksum, fmt.Sprintf("stored 0x%08x, computed 0x%08x", stored, computed)},
		{"body of 2 bytes", record([]byte{0x01, 0x02}), ErrPayload, "body of 2 bytes is shorter than 3"},
		{"8 zero bytes", make([]byte, 8), ErrPayload, "body of 0 bytes is shorter than 3"},
		{"version 2", setByte(0, 2), ErrVersion, "2"},
		{"version 0", setByte(0, 0), ErrVersion, "0"},
		{"kind 4", setByte(1, 4), ErrKind, "4"},
		{"kind 0", setByte(1, 0), ErrKind, "0"},
		{"offset 0", record(cat([]byte{0x01, 0x02, 0x00}, afterOffset)), ErrPayload, "offset is 0"},
		{"offset 80 00", record(cat([]byte{0x01, 0x02, 0x80, 0x00}, afterOffset)), ErrPayload, "offset: non-minimal varint"},
		{"offset 11 ff", record(cat([]byte{0x01, 0x02}, rep(0xff, 11), afterOffset)), ErrPayload, "offset: varint overflows uint64"},
		{"offset cut", record([]byte{0x01, 0x02, 0x80}), ErrPayload, "offset: truncated"},
		{"ref.number cut", record([]byte{0x01, 0x02, 0x01}), ErrPayload, "ref.number: truncated"},
		{"ref.hash cut", record(cat([]byte{0x01, 0x02, 0x01, 0x05}, rep(0x11, 31))), ErrPayload, "ref.hash: truncated"},
		{"ref.parent cut", record(rb[:3+1+32+10]), ErrPayload, "ref.parent: truncated"},
		{"ref.time cut", record(rb[:3+1+64]), ErrPayload, "ref.time: truncated"},
		{"ref.time overflows", record(cat(rb[:3+1+64], rep(0xff, 11))), ErrPayload, "ref.time: varint overflows uint64"},
		{"trailing byte", record(cat(rb, []byte{0x00})), ErrTrailing, "1 bytes after the payload"},
		{"trailing bytes after a block", record(cat(blockHead, []byte{0x00, 0x00, 0x07, 0x07})), ErrTrailing, "2 bytes after the payload"},
		{"trailing byte after logs", record(cat(fbBody, []byte{0x00})), ErrTrailing, "1 bytes after the payload"},
		{"flags cut", record(blockHead), ErrPayload, "flags: truncated"},
		{"flags 02", record(cat(blockHead, []byte{0x02, 0x00})), ErrPayload, "flags 0x02 has unknown bits"},
		{"flags 80", record(cat(blockHead, []byte{0x80, 0x00})), ErrPayload, "flags 0x80 has unknown bits"},
		{"nlogs cut", record(cat(blockHead, []byte{0x00})), ErrPayload, "nlogs: truncated"},
		{"nlogs non-minimal", record(cat(blockHead, []byte{0x00, 0x80, 0x00})), ErrPayload, "nlogs: non-minimal varint"},
		{"nlogs 1000 in 200 bytes", record(nlogs1000Body), ErrPayload, fmt.Sprintf("1000 logs cannot fit in %d bytes", 200-len(nlogs1000))},
		{"nlogs max", record(cat(blockHead, []byte{0x00}, uv(math.MaxUint64), make([]byte, 100))), ErrPayload, "18446744073709551615 logs cannot fit in 100 bytes"},
		{"one log in 55 bytes", record(cat(blockHead, []byte{0x00, 0x01}, minLog[:55])), ErrPayload, "1 logs cannot fit in 55 bytes"},
		{"ntopics 5", record(cat(blockHead, []byte{0x00, 0x01}, rep(0xaa, 20), []byte{0x05}, make([]byte, 160))), ErrPayload, "log 0: 5 topics, at most 4"},
		{"topics cut", record(cat(blockHead, []byte{0x00, 0x01}, rep(0xaa, 20), []byte{0x04}, make([]byte, 60))), ErrPayload, "log[0].topics: truncated"},
		{"data cut", record(cat(blockHead, []byte{0x00, 0x01}, rep(0xaa, 20), []byte{0x00}, uv(100), make([]byte, 40))), ErrPayload, "log[0].data: truncated"},
		{"data length overflows", record(cat(blockHead, []byte{0x00, 0x01}, rep(0xaa, 20), []byte{0x00}, rep(0xff, 11), make([]byte, 40))), ErrPayload, "log[0].data: varint overflows uint64"},
		{"txindex overflows", record(cat(blockHead, []byte{0x00, 0x01}, rep(0xaa, 20), []byte{0x00, 0x00}, rep(0xbb, 32), rep(0xff, 11))), ErrPayload, "log[0].txindex: varint overflows uint64"},
		{"tx index 2^32", record(cat(blockHead, []byte{0x00, 0x01}, rep(0xaa, 20), []byte{0x00, 0x00}, rep(0xbb, 32), uv(1<<32), []byte{0x00})), ErrPayload, "log 0: tx index 4294967296 exceeds 4294967295"},
		{"index 2^32", record(cat(blockHead, []byte{0x00, 0x01}, rep(0xaa, 20), []byte{0x00, 0x00}, rep(0xbb, 32), []byte{0x00}, uv(1<<32))), ErrPayload, "log 0: index 4294967296 exceeds 4294967295"},
		{"index cut", record(cat(blockHead, []byte{0x00, 0x01}, rep(0xaa, 20), []byte{0x00, 0x05}, rep(0x01, 5), rep(0xbb, 32), []byte{0x00, 0x80})), ErrPayload, "log[0].index: truncated"},
		{"log 1 txhash cut", record(cat(
			blockHead, []byte{0x00, 0x02},
			rep(0xaa, 20), []byte{0x00}, uv(60), rep(0x01, 60), rep(0xbb, 32), []byte{0x00, 0x00},
			rep(0xaa, 20), []byte{0x00, 0x00}, rep(0xbb, 10),
		)), ErrPayload, "log[1].txhash: truncated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, n, err := Decode(tt.src)
			if !errors.Is(err, tt.err) {
				t.Fatalf("Decode error = %v, want one wrapping %v", err, tt.err)
			}
			if got, want := err.Error(), tt.err.Error()+": "+tt.detail; got != want {
				t.Errorf("error text = %q, want %q", got, want)
			}
			matched := 0
			for _, ce := range codecErrors {
				if errors.Is(err, ce) {
					matched++
				}
			}
			if matched != 1 {
				t.Errorf("error wraps %d codec errors, want 1", matched)
			}
			if n != 0 || !reflect.DeepEqual(e, Entry{}) {
				t.Errorf("Decode = %+v, %d; want a zero entry and 0", e, n)
			}
		})
	}
}

func TestDecodeValues(t *testing.T) {
	fb, _ := finalBlock()
	rec := Encode(nil, fb)
	src := cat(rec, []byte{0xde, 0xad}) // bytes after the record are not read
	got, n, err := Decode(src)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if n != len(rec) {
		t.Errorf("n = %d, want %d", n, len(rec))
	}
	if !reflect.DeepEqual(got, fb) {
		t.Errorf("Decode =\n%+v\nwant\n%+v", got, fb)
	}
	if got.To != (chain.BlockRef{}) || len(got.Block.Logs) != 1 ||
		got.Block.Logs[0].BlockNumber != 7 || got.Block.Logs[0].BlockHash != blk(7, 0).Hash {
		t.Errorf("Decode: To %v, logs %+v; want zero To and one log of block 7", got.To, got.Block.Logs)
	}
	clear(src)
	if !reflect.DeepEqual(got, fb) {
		t.Error("zeroing src changed the decoded entry")
	}

	empty := Entry{Offset: 9, Kind: KindBlock, Block: chain.Block{Ref: blk(9, 0), Logs: []chain.Log{}}}
	got, _, err = Decode(Encode(nil, empty))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Block.Logs != nil {
		t.Errorf("block without logs: Logs = %#v, want nil", got.Block.Logs)
	}

	bare := Entry{Offset: 9, Kind: KindBlock, Block: chain.Block{Ref: blk(9, 0), Logs: []chain.Log{{
		Topics: []common.Hash{}, Data: []byte{}, Index: 4,
	}}}}
	got, _, err = Decode(Encode(nil, bare))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if l := got.Block.Logs[0]; l.Topics != nil || l.Data != nil || l.Index != 4 {
		t.Errorf("log without topics and data: Topics %#v, Data %#v, Index %d; want nil, nil, 4", l.Topics, l.Data, l.Index)
	}

	mixed := mkLogs(blk(9, 0), 3, 2, 5)
	mixed[1].Topics, mixed[1].Data = nil, nil
	got, _, err = Decode(Encode(nil, Entry{Offset: 9, Kind: KindBlock, Block: chain.Block{Ref: blk(9, 0), Logs: mixed}}))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if l := got.Block.Logs[1]; l.Topics != nil || l.Data != nil {
		t.Errorf("bare log between logs with topics and data: Topics %#v, Data %#v; want nil, nil", l.Topics, l.Data)
	}
	if l := got.Block.Logs[2]; len(l.Topics) != 2 || len(l.Data) != 5 {
		t.Errorf("log 2: %d topics, %d data bytes; want 2 and 5", len(l.Topics), len(l.Data))
	}

	for _, kind := range []Kind{KindRevert, KindFinalize} {
		e := Entry{Offset: 77, Kind: kind, To: blk(12, 3)}
		got, _, err := Decode(Encode(nil, e))
		if err != nil {
			t.Fatalf("Decode %v: %v", kind, err)
		}
		if !reflect.DeepEqual(got, e) {
			t.Errorf("Decode %v = %+v, want %+v", kind, got, e)
		}
	}

	ten := Encode(nil, Entry{Offset: 5, Kind: KindBlock, Block: chain.Block{Ref: blk(7, 0), Logs: mkLogs(blk(7, 0), 10, 3, 64)}})
	if a := testing.AllocsPerRun(100, func() { _, _, _ = Decode(ten) }); a > 3 {
		t.Errorf("Decode of a 10-log block: %v allocs, want at most 3", a)
	}
	rv := Encode(nil, revertR())
	if a := testing.AllocsPerRun(100, func() { _, _, _ = Decode(rv) }); a != 0 {
		t.Errorf("Decode of a revert: %v allocs, want 0", a)
	}

	two, _, err := Decode(Encode(nil, Entry{Offset: 5, Kind: KindBlock, Block: chain.Block{Ref: blk(7, 0), Logs: mkLogs(blk(7, 0), 2, 2, 3)}}))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	logs := two.Block.Logs
	t1, d1 := logs[1].Topics[0], logs[1].Data[0]
	logs[0].Topics = append(logs[0].Topics, common.Hash{0xee})
	logs[0].Data = append(logs[0].Data, 0xee)
	if logs[1].Topics[0] != t1 || logs[1].Data[0] != d1 {
		t.Error("appending to log 0's Topics or Data changed log 1's")
	}
}

func TestDecodeMaxRecordSize(t *testing.T) {
	ref := blk(7, 0)
	sized := func(dataLen int) Entry {
		l := chain.Log{Data: make([]byte, dataLen)}
		return Entry{Offset: 1, Kind: KindBlock, Block: chain.Block{Ref: ref, Logs: []chain.Log{l}}}
	}
	fixed := len(Encode(nil, sized(0))) - 8 - 1 // the body without its one-byte data length
	exact := sized(MaxRecordSize - fixed - 4)   // a 4-byte data length
	rec := Encode(nil, exact)
	if len(rec) != 8+MaxRecordSize {
		t.Fatalf("test setup: record of %d bytes, want %d", len(rec), 8+MaxRecordSize)
	}
	if _, n, err := Decode(rec); err != nil || n != 8+MaxRecordSize {
		t.Errorf("Decode of a body of exactly MaxRecordSize = %d, %v; want %d, nil", n, err, 8+MaxRecordSize)
	}
	over := Encode(nil, sized(MaxRecordSize-fixed-3))
	if _, _, err := Decode(over); !errors.Is(err, ErrRecordTooLarge) {
		t.Errorf("Decode of a body of MaxRecordSize+1: %v, want ErrRecordTooLarge", err)
	}
}

// roundTripEntries cover offsets and numbers at varint boundaries, 0 to 10 logs, 0 to 4 topics,
// data up to 70,000 bytes, the largest indexes, both Final values, a revert and a finalize.
func roundTripEntries() []Entry {
	ref := blk(7, 0)
	maxRef := chain.BlockRef{Number: math.MaxUint64, Hash: h(1, 1), Parent: h(2, 2), Time: math.MaxUint64}
	var es []Entry
	for _, off := range []Offset{1, 127, 128, math.MaxUint64} {
		es = append(es, Entry{Offset: off, Kind: KindRevert, To: ref})
	}
	for _, n := range []uint64{0, math.MaxUint64} {
		r := ref
		r.Number = n
		es = append(es,
			Entry{Offset: 2, Kind: KindBlock, Block: chain.Block{Ref: r}},
			Entry{Offset: 2, Kind: KindFinalize, To: r},
		)
	}
	es = append(es, Entry{Offset: 3, Kind: KindRevert, To: maxRef})
	for _, nlogs := range []int{0, 1, 10} {
		for _, final := range []bool{false, true} {
			es = append(es, Entry{Offset: 4, Kind: KindBlock, Final: final, Block: chain.Block{Ref: ref, Logs: mkLogs(ref, nlogs, 2, 5)}})
		}
	}
	for nt := range 5 {
		es = append(es, Entry{Offset: 5, Kind: KindBlock, Block: chain.Block{Ref: ref, Logs: mkLogs(ref, 2, nt, 1)}})
	}
	for _, dl := range []int{0, 1, 70_000} {
		es = append(es, Entry{Offset: 6, Kind: KindBlock, Block: chain.Block{Ref: ref, Logs: mkLogs(ref, 3, 1, dl)}})
	}
	mixed := mkLogs(ref, 4, 2, 5)
	mixed[1].Topics, mixed[1].Data = nil, nil
	mixed[3].Topics, mixed[3].Data = []common.Hash{}, []byte{}
	es = append(es, Entry{Offset: 6, Kind: KindBlock, Block: chain.Block{Ref: ref, Logs: mixed}})
	big := mkLogs(ref, 1, 4, 3)
	big[0].TxIndex, big[0].Index = math.MaxUint32, math.MaxUint32
	es = append(es,
		Entry{Offset: 7, Kind: KindBlock, Final: true, Block: chain.Block{Ref: maxRef, Logs: big}},
		Entry{Offset: 8, Kind: KindBlock, Block: chain.Block{Ref: ref, Logs: []chain.Log{}}},
		Entry{Offset: 9, Kind: KindBlock, Block: chain.Block{Ref: ref, Logs: []chain.Log{{Topics: []common.Hash{}, Data: []byte{}}}}},
		Entry{Offset: 10, Kind: KindFinalize, To: maxRef},
	)
	return es
}

func TestRoundTrip(t *testing.T) {
	for i, e := range roundTripEntries() {
		t.Run(fmt.Sprintf("%d-%v", i, e.Kind), func(t *testing.T) {
			rec := Encode(nil, e)
			got, n, err := Decode(rec)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if n != len(rec) {
				t.Errorf("n = %d, want %d", n, len(rec))
			}
			if want := normalize(e); !reflect.DeepEqual(got, want) {
				t.Errorf("Decode(Encode(e)) =\n%+v\nwant\n%+v", got, want)
			}
			if again := Encode(nil, got); !bytes.Equal(again, rec) {
				t.Error("Encode of the decoded entry differs from the record")
			}
		})
	}
}

func TestRecordBodySize(t *testing.T) {
	for i, e := range roundTripEntries() {
		e.Offset = math.MaxUint64
		want := len(Encode(nil, e)) - 8
		for _, off := range []Offset{1, 300, math.MaxUint64} {
			e.Offset = off
			if got := recordBodySize(e); got != want {
				t.Errorf("entry %d (%v), offset %d: recordBodySize = %d, want %d", i, e.Kind, off, got, want)
			}
		}
	}
	e := Entry{Offset: 1, Kind: KindBlock, Block: chain.Block{Ref: blk(7, 0), Logs: mkLogs(blk(7, 0), 10, 3, 64)}}
	if a := testing.AllocsPerRun(100, func() { recordBodySize(e) }); a != 0 {
		t.Errorf("recordBodySize: %v allocs, want 0", a)
	}
}
