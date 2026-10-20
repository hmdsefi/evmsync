// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package invariant

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash"
	"maps"
	"math"
	"slices"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/consumer"
	"github.com/hmdsefi/evmsync/internal/fakechain"
)

// Key identifies a balance: a token contract and an account.
type Key struct{ Token, Account common.Address }

// Balances is the reference state: per-token balances from Transfer logs plus a running hash of
// every applied log.
type Balances struct {
	Bal  map[Key]int64 // zero balances are absent
	Hash common.Hash   // running hash of every applied log, with its block
	Logs uint64        // logs applied
}

// NewBalances returns the empty state.
func NewBalances() Balances {
	return Balances{Bal: map[Key]int64{}}
}

// hasher is a reusable Keccak-256 state with a scratch buffer.
type hasher struct {
	h   hash.Hash
	buf []byte
}

// hasherPool keeps ApplyBalances from allocating a Keccak state per block.
var hasherPool = sync.Pool{
	New: func() any { return &hasher{h: sha3.NewLegacyKeccak256()} },
}

// ApplyBalances applies b's logs to s in order, modifying and returning s. Every log extends the
// running hash with its block number and hash, index, address, topics and data, so a log applied
// from an orphan differs from the same log in its canonical block. A Transfer log with three
// topics and a 32-byte value of at most math.MaxInt64 moves the value from the first indexed
// account to the second; balances may go negative.
func ApplyBalances(s Balances, b chain.Block) Balances {
	if s.Bal == nil {
		s.Bal = map[Key]int64{}
	}
	hs := hasherPool.Get().(*hasher) //nolint:errcheck // the pool only holds *hasher
	defer hasherPool.Put(hs)
	h, buf := hs.h, hs.buf
	for i := range b.Logs {
		l := &b.Logs[i]
		buf = append(buf[:0], s.Hash[:]...)
		buf = binary.BigEndian.AppendUint64(buf, b.Ref.Number)
		buf = append(buf, b.Ref.Hash[:]...)
		buf = binary.BigEndian.AppendUint32(buf, l.Index)
		buf = append(buf, l.Address[:]...)
		buf = append(buf, byte(len(l.Topics))) //nolint:gosec // a log has at most 4 topics
		for _, tp := range l.Topics {
			buf = append(buf, tp[:]...)
		}
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(l.Data))) //nolint:gosec // block data is far below 4 GiB
		buf = append(buf, l.Data...)
		h.Reset()
		h.Write(buf)
		h.Sum(s.Hash[:0])
		s.Logs++
		hs.buf = buf

		v, ok := transferValue(l)
		if !ok {
			continue
		}
		from := Key{l.Address, common.BytesToAddress(l.Topics[1][12:])}
		to := Key{l.Address, common.BytesToAddress(l.Topics[2][12:])}
		add(s.Bal, from, -v)
		add(s.Bal, to, v)
	}
	return s
}

// transferValue returns the value a Transfer log moves, and false for any other log.
func transferValue(l *chain.Log) (int64, bool) {
	if len(l.Topics) != 3 || l.Topics[0] != fakechain.TransferTopic || len(l.Data) != 32 {
		return 0, false
	}
	if !bytes.Equal(l.Data[:24], make([]byte, 24)) {
		return 0, false
	}
	v := binary.BigEndian.Uint64(l.Data[24:])
	if v > math.MaxInt64 {
		return 0, false
	}
	return int64(v), true
}

// add adds d to bal[k] and deletes the entry when it becomes 0.
func add(bal map[Key]int64, k Key, d int64) {
	if v := bal[k] + d; v != 0 {
		bal[k] = v
	} else {
		delete(bal, k)
	}
}

// CloneBalances returns a copy of s that shares no memory with it.
func CloneBalances(s Balances) Balances {
	s.Bal = maps.Clone(s.Bal)
	if s.Bal == nil {
		s.Bal = map[Key]int64{}
	}
	return s
}

// Digest returns the 32-byte Keccak-256 of the running hash, the log count, and every balance in
// ascending order of token, then account. Equal states give equal digests.
func (s Balances) Digest() []byte {
	keys := slices.SortedFunc(maps.Keys(s.Bal), func(a, b Key) int {
		if c := bytes.Compare(a.Token[:], b.Token[:]); c != 0 {
			return c
		}
		return bytes.Compare(a.Account[:], b.Account[:])
	})
	buf := make([]byte, 0, 40+len(keys)*48)
	buf = append(buf, s.Hash[:]...)
	buf = binary.BigEndian.AppendUint64(buf, s.Logs)
	for _, k := range keys {
		buf = append(buf, k.Token[:]...)
		buf = append(buf, k.Account[:]...)
		buf = binary.BigEndian.AppendUint64(buf, uint64(s.Bal[k])) //nolint:gosec // two's complement on purpose
	}
	h := sha3.NewLegacyKeccak256()
	h.Write(buf)
	return h.Sum(nil)
}

// ExpectedBalances folds ApplyBalances over blocks from NewBalances and returns the digest. It is
// the oracle for a subject's state after the final canonical chain.
func ExpectedBalances(blocks []chain.Block) []byte {
	s := NewBalances()
	for _, b := range blocks {
		s = ApplyBalances(s, b)
	}
	return s.Digest()
}

// memorySubject is a consumer.Memory over Balances.
type memorySubject struct {
	*consumer.Memory[Balances]
}

// Digest returns the digest of the current state.
func (s memorySubject) Digest(context.Context) ([]byte, error) {
	return s.State().Digest(), nil
}

// NewMemorySubject returns a consumer.Memory[Balances] subject, with ApplyBalances and
// CloneBalances, whose Digest is State().Digest().
func NewMemorySubject() Subject {
	return memorySubject{consumer.NewMemory(NewBalances(), ApplyBalances, CloneBalances)}
}
