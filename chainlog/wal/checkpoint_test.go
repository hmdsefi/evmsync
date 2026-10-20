// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// ref returns block n of chain f, linked to block n-1 of the same chain.
func ref(n uint64, f byte) chain.BlockRef {
	r := chain.BlockRef{Number: n, Time: 1_700_000_000 + 12*n}
	r.Hash[0] = 0xb0 | f
	binary.BigEndian.PutUint64(r.Hash[24:], n)
	if n > 0 {
		r.Parent[0] = 0xb0 | f
		binary.BigEndian.PutUint64(r.Parent[24:], n-1)
	}
	return r
}

// testState is the state after Finalize 2 and blocks 3, 4, 5: a window of 3.
func testState() chainlog.State {
	s := chainlog.NewState(0)
	s.Apply(chainlog.Entry{Kind: chainlog.KindFinalize, To: ref(2, 0)})
	for n := uint64(3); n <= 5; n++ {
		s.Apply(chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: ref(n, 0)}})
	}
	return s
}

type stateView struct {
	tip, fin chain.BlockRef
	win      []chain.BlockRef
}

func viewOf(s chainlog.State) stateView { return stateView{s.Tip(), s.Finalized(), s.Window()} }

func TestCheckpointRoundTrip(t *testing.T) {
	c := checkpoint{x: 300, l: 7, state: testState()}
	b := encodeCheckpoint(c)
	stateBin, _ := c.state.MarshalBinary()
	if want := seal(cat([]byte("EVMSCKPT"), []byte{0x01}, uv(300), uv(7), stateBin)); !bytes.Equal(b, want) {
		t.Fatalf("encodeCheckpoint =\n%x\nwant\n%x", b, want)
	}
	got, err := decodeCheckpoint(b, 0)
	if err != nil {
		t.Fatalf("decodeCheckpoint: %v", err)
	}
	if got.x != 300 || got.l != 7 || !reflect.DeepEqual(viewOf(got.state), viewOf(c.state)) {
		t.Errorf("decodeCheckpoint = x %d, l %d, %+v; want 300, 7, %+v", got.x, got.l, viewOf(got.state), viewOf(c.state))
	}

	// The state gets the cap Open asks for, also when its window is already longer.
	small, err := decodeCheckpoint(b, 2)
	if err != nil {
		t.Fatalf("decodeCheckpoint with cap 2: %v", err)
	}
	next := chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: ref(6, 0)}}
	if err := small.state.Check(next); err == nil || !strings.HasSuffix(err.Error(), "window is full (2 blocks)") {
		t.Errorf("Check after decoding with cap 2 = %v, want a full window of 2 blocks", err)
	}

	empty, err := decodeCheckpoint(encodeCheckpoint(checkpoint{state: chainlog.NewState(0)}), 0)
	if err != nil || empty.x != 0 || empty.l != 0 || !reflect.DeepEqual(viewOf(empty.state), stateView{}) {
		t.Errorf("empty checkpoint decoded as %+v, %v", empty, err)
	}
}

func TestCheckpointCorrupt(t *testing.T) {
	stateBin, _ := testState().MarshalBinary()
	valid := encodeCheckpoint(checkpoint{x: 9, l: 7, state: testState()})
	flipped := bytes.Clone(valid)
	flipped[len(flipped)-3] ^= 0x01
	tooLarge := binary.LittleEndian.AppendUint32(nil, 1073741825)
	tooLarge = append(tooLarge, 0, 0, 0, 0)
	badState := bytes.Clone(stateBin)
	badState[0] = 2
	head := cat([]byte("EVMSCKPT"), []byte{0x01})

	tests := []struct {
		name   string
		data   []byte
		detail string
	}{
		{"cut", valid[:len(valid)-1], "truncated"},
		{"header cut", valid[:7], "truncated"},
		{"flipped byte", flipped, "checksum mismatch"},
		{"bad magic", seal(cat([]byte("EVMSMETA"), []byte{0x01}, uv(9), uv(7), stateBin)), "bad magic"},
		{"version 2", seal(cat([]byte("EVMSCKPT"), []byte{0x02}, uv(9), uv(7), stateBin)), "version 2"},
		{"body cut in the magic", seal([]byte("EVMS")), "truncated"},
		{"l above x", seal(cat(head, uv(7), uv(9), stateBin)), "bad header"},
		{"x non-minimal", seal(cat(head, []byte{0x89, 0x00}, uv(7), stateBin)), "bad header"},
		{"l cut", seal(cat(head, uv(9))), "bad header"},
		{"bad state", seal(cat(head, uv(9), uv(7), badState)), "chainlog: bad state encoding: version 2"},
		{"length 1073741825", tooLarge, "too large"},
		{"trailing byte after the body", cat(valid, []byte{0x00}), "trailing bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeCheckpoint(tt.data, 0)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("decodeCheckpoint error = %v, want one wrapping ErrCorrupt", err)
			}
			if got, want := err.Error(), "wal: corrupt log: checkpoint: "+tt.detail; got != want {
				t.Errorf("error = %q, want %q", got, want)
			}
		})
	}
	_, err := decodeCheckpoint(seal(cat(head, uv(9), uv(7), badState)), 0)
	if !errors.Is(err, chainlog.ErrBadState) {
		t.Errorf("bad state error = %v, want one also wrapping chainlog.ErrBadState", err)
	}
}
