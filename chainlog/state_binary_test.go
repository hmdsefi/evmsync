// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/hmdsefi/evmsync/chain"
)

// stateBytes is the binary form of a state with these refs, built by hand.
func stateBytes(tip, fin chain.BlockRef, win ...chain.BlockRef) []byte {
	b := cat([]byte{0x01, 0x00}, refBytes(tip), refBytes(fin), uv(uint64(len(win))))
	for _, r := range win {
		b = append(b, refBytes(r)...)
	}
	return b
}

func TestStateBinary(t *testing.T) {
	empty, _ := State{}.MarshalBinary()
	if want := cat([]byte{0x01}, make([]byte, 134)); !bytes.Equal(empty, want) {
		t.Errorf("empty state = %x, want 01 and 134 zero bytes", empty)
	}

	// The empty state, every state of the apply sequence, and one after a Final block.
	var states []State
	var s State
	states = append(states, s)
	for _, st := range applySteps() {
		s.Apply(st.e)
		states = append(states, s.Clone())
	}
	s.Apply(finalBlk(blk(12, 1)))
	states = append(states, s.Clone())
	for i, s := range states {
		b, err := s.MarshalBinary()
		if err != nil {
			t.Fatalf("state %d: MarshalBinary: %v", i, err)
		}
		if want := stateBytes(s.Tip(), s.Finalized(), s.Window()...); !bytes.Equal(b, want) {
			t.Errorf("state %d: MarshalBinary =\n%x\nwant\n%x", i, b, want)
		}
		u := NewState(3)
		if err := u.UnmarshalBinary(b); err != nil {
			t.Fatalf("state %d: UnmarshalBinary: %v", i, err)
		}
		wantState(t, u, s.Tip(), s.Finalized(), s.Window())
	}

	b1, b2, b3 := blk(1, 0), blk(2, 0), blk(3, 0)
	zero := chain.BlockRef{}
	valid := stateBytes(b2, b1, b2)
	late := b2
	late.Time++
	big := chain.BlockRef{Number: 300, Hash: h(300, 0)} // a two-byte number and time 0: 67 bytes
	tests := []struct {
		name   string
		data   []byte
		detail string
	}{
		{"empty input", nil, "version: truncated"},
		{"flags cut", []byte{0x01}, "flags: truncated"},
		{"tip cut", []byte{0x01, 0x00}, "tip: truncated"},
		{"tip hash cut", cat([]byte{0x01, 0x00, 0x05}, rep(0x11, 10)), "tip: truncated"},
		{"tip varint non-minimal", cat([]byte{0x01, 0x00, 0x80, 0x00}, make([]byte, 70)), "tip: bad varint"},
		{"tip varint overflows", cat([]byte{0x01, 0x00}, rep(0xff, 11), make([]byte, 70)), "tip: bad varint"},
		{"finalized cut", cat([]byte{0x01, 0x00}, refBytes(zero), []byte{0x00}, make([]byte, 64)), "finalized: truncated"},
		{"finalized time non-minimal", cat([]byte{0x01, 0x00}, refBytes(zero), make([]byte, 65), []byte{0x81, 0x00}), "finalized: bad varint"},
		{"nwindow cut", cat([]byte{0x01, 0x00}, refBytes(zero), refBytes(zero)), "nwindow: truncated"},
		{"nwindow non-minimal", cat([]byte{0x01, 0x00}, refBytes(zero), refBytes(zero), []byte{0x80, 0x00}), "nwindow: bad varint"},
		{"version 2", cat([]byte{0x02}, valid[1:]), "version 2"},
		{"version 0", cat([]byte{0x00}, valid[1:]), "version 0"},
		{"flags 01", cat([]byte{0x01, 0x01}, valid[2:]), "flags 0x01"},
		{"flags 80", cat([]byte{0x01, 0x80}, valid[2:]), "flags 0x80"},
		{"3 refs in 100 bytes", cat([]byte{0x01, 0x00}, refBytes(zero), refBytes(zero), []byte{0x03}, make([]byte, 100)), "3 window refs cannot fit in 100 bytes"},
		{"2 refs in 131 bytes", cat([]byte{0x01, 0x00}, refBytes(zero), refBytes(zero), []byte{0x02}, make([]byte, 131)), "2 window refs cannot fit in 131 bytes"},
		{"window[1] cut", cat([]byte{0x01, 0x00}, refBytes(zero), refBytes(zero), []byte{0x02}, refBytes(big), refBytes(b2)[:65]), "window[1]: truncated"},
		{"window[0] varint", cat([]byte{0x01, 0x00}, refBytes(zero), refBytes(zero), []byte{0x01, 0x80, 0x00}, make([]byte, 66)), "window[0]: bad varint"},
		{"trailing byte", cat(valid, []byte{0x00}), "1 trailing bytes"},
		{"window not ascending", stateBytes(b2, zero, b3, b2), "window is not ascending"},
		{"window repeats a number", stateBytes(b2, zero, b2, b2), "window is not ascending"},
		{"window at F", stateBytes(b3, b2, b2, b3), "window ref 2 is not above finalized 2"},
		{"window below F", stateBytes(b3, b3, b1, b2), "window ref 1 is not above finalized 3"},
		{"tip not last", stateBytes(b1, zero, b1, b2), "tip " + b1.String() + " is not the last window ref"},
		{"tip not F", stateBytes(b3, b2), "tip " + b3.String() + " is not the finalized ref"},
		{"tip without F", stateBytes(b3, zero), "tip " + b3.String() + " is not the finalized ref"},
		{"tip differs from the last ref in time only", stateBytes(late, zero, b1, b2), "tip " + late.String() + " is not the last window ref"},
		{"tip differs from F in time only", stateBytes(late, b2), "tip " + late.String() + " is not the finalized ref"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := build(t, 0, blocks(4, 6)...)
			before := snap(u)
			err := u.UnmarshalBinary(tt.data)
			if !errors.Is(err, ErrBadState) {
				t.Fatalf("UnmarshalBinary error = %v, want one wrapping ErrBadState", err)
			}
			if got, want := err.Error(), "chainlog: bad state encoding: "+tt.detail; got != want {
				t.Errorf("error text =\n%q\nwant\n%q", got, want)
			}
			if after := snap(u); !reflect.DeepEqual(after, before) {
				t.Errorf("a failed UnmarshalBinary changed the state")
			}
		})
	}

	// A window that starts at block 0 with no F, as on a chain indexed from genesis.
	genesis := build(t, 0, block(blk(0, 0)), block(blk(1, 0)))
	gb, _ := genesis.MarshalBinary()
	var gu State
	if err := gu.UnmarshalBinary(gb); err != nil {
		t.Fatalf("UnmarshalBinary of a window from block 0: %v", err)
	}
	wantState(t, gu, blk(1, 0), zero, refs(blk(0, 0), blk(1, 0)))

	// The smallest refs are 66 bytes: two of them fit in exactly 132.
	r1 := chain.BlockRef{Number: 1, Hash: h(1, 0), Parent: h(0, 0)}
	r2 := chain.BlockRef{Number: 2, Hash: h(2, 0), Parent: h(1, 0)}
	small := stateBytes(r2, zero, r1, r2)
	if rest := len(small) - (2 + 66 + 66 + 1); rest != 132 {
		t.Fatalf("test setup: window of %d bytes, want 132", rest)
	}
	var u2 State
	if err := u2.UnmarshalBinary(small); err != nil {
		t.Fatalf("UnmarshalBinary of two 66-byte refs: %v", err)
	}
	wantState(t, u2, r2, zero, refs(r1, r2))

	// A window longer than the receiver's cap is accepted; the next block then fails.
	five := build(t, 0, blocks(1, 5)...)
	b, _ := five.MarshalBinary()
	u := NewState(3)
	if err := u.UnmarshalBinary(b); err != nil {
		t.Fatalf("UnmarshalBinary of a window of 5 into NewState(3): %v", err)
	}
	wantState(t, u, blk(5, 0), chain.BlockRef{}, five.Window())
	wantMalformed(t, u.Check(block(blk(6, 0))), "block 6: window is full (3 blocks)")
}
