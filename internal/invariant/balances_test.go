// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package invariant

import (
	"bytes"
	"encoding/binary"
	"maps"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/internal/fakechain"
)

// word returns v as a 32-byte big-endian value.
func word(v uint64) []byte {
	w := make([]byte, 32)
	binary.BigEndian.PutUint64(w[24:], v)
	return w
}

// topic returns a as an indexed event argument.
func topic(a common.Address) common.Hash {
	var h common.Hash
	copy(h[12:], a[:])
	return h
}

func transfer(token common.Address, from, to int, data []byte) chain.Log {
	return chain.Log{
		Address: token,
		Topics:  []common.Hash{fakechain.TransferTopic, topic(fakechain.Account(from)), topic(fakechain.Account(to))},
		Data:    data,
	}
}

// keccakOf hashes the concatenation of parts.
func keccakOf(parts ...[]byte) common.Hash {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return common.BytesToHash(h.Sum(nil))
}

// logLink computes one step of the running hash from the byte layout.
func logLink(prev common.Hash, ref chain.BlockRef, l chain.Log) common.Hash {
	var b bytes.Buffer
	b.Write(prev[:])
	b.Write(binary.BigEndian.AppendUint64(nil, ref.Number))
	b.Write(ref.Hash[:])
	b.Write(binary.BigEndian.AppendUint32(nil, l.Index))
	b.Write(l.Address[:])
	b.WriteByte(byte(len(l.Topics))) //nolint:gosec // at most 4 topics
	for _, tp := range l.Topics {
		b.Write(tp[:])
	}
	b.Write(binary.BigEndian.AppendUint32(nil, uint32(len(l.Data)))) //nolint:gosec // test data is small
	b.Write(l.Data)
	return keccakOf(b.Bytes())
}

func TestBalances(t *testing.T) {
	want := common.HexToHash("0xdaa77426c30c02a43d9fba4e841a6556c524d47030762eb14dc4af897e605d9b")
	if got := NewBalances().Digest(); !bytes.Equal(got, want[:]) {
		t.Errorf("NewBalances().Digest() = %x, want %x", got, want)
	}

	a0 := fakechain.Address(0)
	high := word(7)
	high[0] = 1
	ref := chain.BlockRef{Number: 9, Hash: common.HexToHash("0x99")}
	logs := []chain.Log{
		transfer(a0, 1, 2, word(5)),
		{Address: a0, Topics: []common.Hash{fakechain.ApprovalTopic, topic(fakechain.Account(1)), topic(fakechain.Account(2))},
			Data: word(3)},
		{Address: a0, Topics: []common.Hash{fakechain.OtherTopic}, Data: word(11)},
		transfer(a0, 3, 4, high),
	}
	for i := range logs {
		logs[i].Index, logs[i].BlockNumber, logs[i].BlockHash = uint32(i), ref.Number, ref.Hash //nolint:gosec // i < 4
	}
	s := ApplyBalances(NewBalances(), chain.Block{Ref: ref, Logs: logs})
	wantBal := map[Key]int64{{a0, fakechain.Account(1)}: -5, {a0, fakechain.Account(2)}: 5}
	if !maps.Equal(s.Bal, wantBal) || s.Logs != 4 {
		t.Errorf("after the four-log block: Bal %v, Logs %d; want %v, 4", s.Bal, s.Logs, wantBal)
	}
	var h common.Hash
	for _, l := range logs {
		h = logLink(h, ref, l)
	}
	if s.Hash != h {
		t.Errorf("Hash = %s, want %s", s.Hash.Hex(), h.Hex())
	}

	// Digest: the running hash, the log count, then the balances sorted by token, then account.
	var d bytes.Buffer
	d.Write(s.Hash[:])
	d.Write(binary.BigEndian.AppendUint64(nil, 4))
	for _, kv := range []struct {
		k Key
		v int64
	}{{Key{a0, fakechain.Account(1)}, -5}, {Key{a0, fakechain.Account(2)}, 5}} {
		d.Write(kv.k.Token[:])
		d.Write(kv.k.Account[:])
		d.Write(binary.BigEndian.AppendUint64(nil, uint64(kv.v))) //nolint:gosec // two's complement on purpose
	}
	if got, want := s.Digest(), keccakOf(d.Bytes()); !bytes.Equal(got, want[:]) {
		t.Errorf("Digest() = %x, want %x", got, want)
	}

	clone := CloneBalances(s)
	back := chain.Block{Ref: chain.BlockRef{Number: 10, Hash: common.HexToHash("0xaa")},
		Logs: []chain.Log{transfer(a0, 2, 1, word(5))}}
	s = ApplyBalances(s, back)
	if len(s.Bal) != 0 || s.Logs != 5 {
		t.Errorf("after the reverse transfer: Bal %v, Logs %d; want empty, 5", s.Bal, s.Logs)
	}
	if !maps.Equal(clone.Bal, wantBal) || clone.Logs != 4 || clone.Hash != h {
		t.Errorf("clone changed with the original: %+v", clone)
	}
}

func TestBalancesIgnores(t *testing.T) {
	a1 := fakechain.Address(1)
	ref := chain.BlockRef{Number: 1, Hash: common.HexToHash("0x01")}
	bad := []struct {
		name string
		l    chain.Log
	}{
		{"value above MaxInt64", transfer(a1, 1, 2, word(1<<63))},
		{"short data", transfer(a1, 1, 2, word(5)[:31])},
		{"long data", transfer(a1, 1, 2, append(word(5), 0))},
		{"two topics", chain.Log{Address: a1, Topics: []common.Hash{fakechain.TransferTopic, topic(fakechain.Account(1))},
			Data: word(5)}},
		{"four topics", chain.Log{Address: a1, Topics: []common.Hash{fakechain.TransferTopic,
			topic(fakechain.Account(1)), topic(fakechain.Account(2)), {}}, Data: word(5)}},
		{"approval", chain.Log{Address: a1, Topics: []common.Hash{fakechain.ApprovalTopic,
			topic(fakechain.Account(1)), topic(fakechain.Account(2))}, Data: word(5)}},
		{"high byte 23", func() chain.Log { w := word(5); w[23] = 1; return transfer(a1, 1, 2, w) }()},
	}
	for _, tt := range bad {
		s := ApplyBalances(NewBalances(), chain.Block{Ref: ref, Logs: []chain.Log{tt.l}})
		if len(s.Bal) != 0 || s.Logs != 1 || s.Hash != logLink(common.Hash{}, ref, tt.l) {
			t.Errorf("%s: Bal %v, Logs %d, want no balance change and one hashed log", tt.name, s.Bal, s.Logs)
		}
	}
	top := ApplyBalances(NewBalances(), chain.Block{Ref: ref, Logs: []chain.Log{transfer(a1, 1, 2, word(1<<63-1))}})
	if top.Bal[Key{a1, fakechain.Account(2)}] != 1<<63-1 {
		t.Errorf("MaxInt64 transfer: Bal %v", top.Bal)
	}
	// The zero Balances, as a store subject might rebuild it, is usable.
	zero := ApplyBalances(Balances{}, chain.Block{Ref: ref, Logs: []chain.Log{transfer(a1, 1, 2, word(4))}})
	if zero.Bal[Key{a1, fakechain.Account(2)}] != 4 {
		t.Errorf("transfer onto the zero Balances: Bal %v", zero.Bal)
	}
	if c := CloneBalances(Balances{}); c.Bal == nil {
		t.Error("CloneBalances(Balances{}) has a nil map")
	}
	self := ApplyBalances(NewBalances(), chain.Block{Ref: ref, Logs: []chain.Log{transfer(a1, 3, 3, word(9))}})
	if len(self.Bal) != 0 {
		t.Errorf("self transfer: Bal %v, want empty", self.Bal)
	}
}

func TestBalancesDigestOrder(t *testing.T) {
	ref := chain.BlockRef{Number: 1, Hash: common.HexToHash("0x01")}
	var logs []chain.Log
	for _, x := range [][3]int{{1, 5, 6}, {0, 7, 2}, {1, 0, 3}, {0, 4, 1}} {
		logs = append(logs, transfer(fakechain.Address(x[0]), x[1], x[2], word(uint64(10+x[1])))) //nolint:gosec // small
	}
	s := ApplyBalances(NewBalances(), chain.Block{Ref: ref, Logs: logs})
	var d bytes.Buffer
	d.Write(s.Hash[:])
	d.Write(binary.BigEndian.AppendUint64(nil, s.Logs))
	for _, tok := range []int{0, 1} {
		for acct := range 8 {
			k := Key{fakechain.Address(tok), fakechain.Account(acct)}
			if v, ok := s.Bal[k]; ok {
				d.Write(k.Token[:])
				d.Write(k.Account[:])
				d.Write(binary.BigEndian.AppendUint64(nil, uint64(v))) //nolint:gosec // two's complement on purpose
			}
		}
	}
	if got, want := s.Digest(), keccakOf(d.Bytes()); !bytes.Equal(got, want[:]) || len(s.Bal) != 8 {
		t.Errorf("Digest() = %x over %d balances, want %x over 8", got, len(s.Bal), want)
	}
}

func TestExpectedBalances(t *testing.T) {
	c := fakechain.New(3, fakechain.Params{})
	c.Mine(40)
	blocks := c.CanonicalBlocks(Filter)
	s := NewBalances()
	for _, b := range blocks {
		s = ApplyBalances(s, b)
	}
	if got := ExpectedBalances(blocks); !bytes.Equal(got, s.Digest()) || s.Logs == 0 {
		t.Errorf("ExpectedBalances = %x, want the fold's digest %x (%d logs)", got, s.Digest(), s.Logs)
	}
	if got := ExpectedBalances(nil); !bytes.Equal(got, NewBalances().Digest()) {
		t.Errorf("ExpectedBalances(nil) = %x, want the empty digest", got)
	}

	sub := NewMemorySubject()
	ctx := t.Context()
	if d, err := sub.Digest(ctx); err != nil || !bytes.Equal(d, NewBalances().Digest()) {
		t.Errorf("new subject Digest = %x, %v", d, err)
	}
}
