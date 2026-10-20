// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"bytes"
	"encoding/binary"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"

	"github.com/hmdsefi/evmsync/chain"
)

// logCounts maps a draw of Intn(10) to the number of fresh logs in a block.
var logCounts = [10]int{0, 0, 0, 1, 1, 1, 2, 2, 3, 4}

// freshLogs draws a new block's fresh logs from the logs stream. Each one is a new transaction.
// The caller sets the block fields and the indexes.
func (c *Chain) freshLogs() []chain.Log {
	r := c.logRand
	n := logCounts[r.Intn(10)]
	logs := make([]chain.Log, 0, n)
	for range n {
		l := chain.Log{Address: Address(int(r.Intn(4)))} //nolint:gosec // below 4
		data := make([]byte, 32)
		if k := r.Intn(10); k <= 7 {
			topic := TransferTopic
			if k >= 6 {
				topic = ApprovalTopic
			}
			from, to := Account(int(r.Intn(8))), Account(int(r.Intn(8))) //nolint:gosec // below 8
			amount := 1 + r.Intn(1000)
			l.Topics = []common.Hash{topic, addressTopic(from), addressTopic(to)}
			binary.BigEndian.PutUint64(data[24:], amount)
		} else {
			l.Topics = []common.Hash{OtherTopic}
			binary.BigEndian.PutUint64(data[24:], r.Uint64())
		}
		l.Data = data
		c.txSerial++
		l.TxHash = txHash(c.txSerial)
		logs = append(logs, l)
	}
	return logs
}

// addressTopic is a as an indexed event argument: 12 zero bytes, then the address.
func addressTopic(a common.Address) common.Hash {
	var h common.Hash
	copy(h[12:], a[:])
	return h
}

// matching returns copies of the logs that pass f, or nil when none does.
func matching(logs []chain.Log, f chain.Filter) []chain.Log {
	return appendMatching(nil, logs, f)
}

// appendMatching appends copies of the logs that pass f to dst and returns the result.
func appendMatching(dst, logs []chain.Log, f chain.Filter) []chain.Log {
	for _, l := range logs {
		if f.Match(l) {
			l.Topics = slices.Clone(l.Topics)
			l.Data = bytes.Clone(l.Data)
			dst = append(dst, l)
		}
	}
	return dst
}

func blockHash(n uint64, parent common.Hash, serial uint64) common.Hash {
	return keccak([]byte("evmsync/fakechain/block"), u64be(n), parent[:], u64be(serial))
}

func txHash(serial uint64) common.Hash {
	return keccak([]byte("evmsync/fakechain/tx"), u64be(serial))
}

// keccak returns the Keccak-256 hash of the concatenated parts.
func keccak(parts ...[]byte) common.Hash {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	var out common.Hash
	h.Sum(out[:0])
	return out
}

func u64be(v uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, v)
}
