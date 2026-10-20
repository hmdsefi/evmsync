// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"

	"github.com/hmdsefi/evmsync/chain"
)

var update = flag.Bool("update", false, "rewrite golden files")

// TestGolden pins the generator: a change to any draw, hash or log layout changes the digests.
// Rewrite testdata/golden.txt with -update only in a commit that says why the generator changed.
func TestGolden(t *testing.T) {
	var buf bytes.Buffer
	for seed := uint64(1); seed <= 5; seed++ {
		c := New(seed, Params{})
		evs := make([]Event, 0, 300)
		for range 300 {
			evs = append(evs, c.Step())
		}
		fmt.Fprintf(&buf, "%d %s\n", seed, chainDigest(c, evs))
	}
	path := filepath.Join("testdata", "golden.txt")
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("generator digests:\n%s\nwant (testdata/golden.txt):\n%s", buf.Bytes(), want)
	}
}

// chainDigest hashes the event lines, then every canonical block's ref and all of its logs.
func chainDigest(c *Chain, evs []Event) string {
	h := sha3.NewLegacyKeccak256()
	for _, e := range evs {
		h.Write([]byte(e.String() + "\n"))
	}
	var u [8]byte
	put := func(v uint64) {
		binary.BigEndian.PutUint64(u[:], v)
		h.Write(u[:])
	}
	g := c.Genesis().Ref
	put(g.Number)
	h.Write(g.Hash[:])
	for _, b := range c.CanonicalBlocks(chain.Filter{}) {
		put(b.Ref.Number)
		h.Write(b.Ref.Hash[:])
		h.Write(b.Ref.Parent[:])
		put(b.Ref.Time)
		put(uint64(len(b.Logs)))
		for _, l := range b.Logs {
			h.Write(l.Address[:])
			put(uint64(len(l.Topics)))
			for _, tp := range l.Topics {
				h.Write(tp[:])
			}
			put(uint64(len(l.Data)))
			h.Write(l.Data)
			h.Write(l.TxHash[:])
			put(uint64(l.TxIndex))
			put(uint64(l.Index))
		}
	}
	put(c.Finalized().Ref.Number)
	return hex.EncodeToString(h.Sum(nil))
}
