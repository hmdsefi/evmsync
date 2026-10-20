// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

const (
	stateVersion = 1
	// minRefSize is the smallest encoded ref: two one-byte varints around the two hashes.
	minRefSize = 1 + 2*common.HashLength + 1
)

// MarshalBinary encodes s: version 1, a flags byte of 0, the tip and F, the window's length as
// a varint and its refs. A ref is its number and time as varints around its hash and parent; the
// zero ref is 66 bytes. The cap is not encoded. MarshalBinary never returns an error.
func (s State) MarshalBinary() ([]byte, error) {
	size := 2 + refSize(s.tip) + refSize(s.fin) + binary.MaxVarintLen64
	for _, r := range s.win {
		size += refSize(r)
	}
	b := make([]byte, 0, size)
	b = append(b, stateVersion, 0)
	b = appendRef(b, s.tip)
	b = appendRef(b, s.fin)
	b = binary.AppendUvarint(b, uint64(len(s.win)))
	for _, r := range s.win {
		b = appendRef(b, r)
	}
	return b, nil
}

// UnmarshalBinary replaces s's tip, F and window with the decoded ones and keeps s's cap. It
// returns an error wrapping ErrBadState on malformed input, including a window that is not
// strictly ascending, not above F, or whose last ref (F when empty) is not the tip, and then
// leaves s unchanged. A window longer than s's cap is accepted; the next non-final block then
// fails Check.
func (s *State) UnmarshalBinary(data []byte) error {
	d := stateDecoder{b: data}
	version, err := d.byte("version")
	if err != nil {
		return err
	}
	if version != stateVersion {
		return badState("version %d", version)
	}
	flags, err := d.byte("flags")
	if err != nil {
		return err
	}
	if flags != 0 {
		return badState("flags 0x%02x", flags)
	}
	tip, err := d.ref("tip", -1)
	if err != nil {
		return err
	}
	fin, err := d.ref("finalized", -1)
	if err != nil {
		return err
	}
	n, err := d.uvarint("nwindow", -1)
	if err != nil {
		return err
	}
	// Bound the window by the remaining bytes before allocating it.
	if n > uint64(len(d.b)/minRefSize) {
		return badState("%d window refs cannot fit in %d bytes", n, len(d.b))
	}
	var win []chain.BlockRef
	if n > 0 {
		win = make([]chain.BlockRef, n)
		for i := range win {
			if win[i], err = d.ref("window", i); err != nil {
				return err
			}
		}
	}
	if len(d.b) > 0 {
		return badState("%d trailing bytes", len(d.b))
	}
	if err := checkDecoded(tip, fin, win); err != nil {
		return err
	}
	s.tip, s.fin, s.win = tip, fin, win
	return nil
}

// checkDecoded checks the invariants every applied sequence keeps: the window is strictly
// ascending and above F, and the tip is its last ref, or F when it is empty.
func checkDecoded(tip, fin chain.BlockRef, win []chain.BlockRef) error {
	for i := 1; i < len(win); i++ {
		if win[i].Number <= win[i-1].Number {
			return badState("window is not ascending")
		}
	}
	if !isZero(fin) && len(win) > 0 && win[0].Number <= fin.Number {
		return badState("window ref %d is not above finalized %d", win[0].Number, fin.Number)
	}
	if len(win) > 0 {
		if tip != win[len(win)-1] {
			return badState("tip %s is not the last window ref", tip)
		}
	} else if tip != fin {
		return badState("tip %s is not the finalized ref", tip)
	}
	return nil
}

// stateDecoder reads the fields of a state encoding in order. Its errors wrap ErrBadState.
type stateDecoder struct {
	b []byte // the bytes not read yet
}

func (d *stateDecoder) byte(field string) (byte, error) {
	if len(d.b) == 0 {
		return 0, badState("%s: truncated", field)
	}
	c := d.b[0]
	d.b = d.b[1:]
	return c, nil
}

// uvarint reads a minimal unsigned varint. field and index name the field in the error (index -1:
// not a window ref).
func (d *stateDecoder) uvarint(field string, index int) (uint64, error) {
	x, n := binary.Uvarint(d.b)
	switch {
	case n == 0:
		return 0, badState("%s: truncated", stateField(field, index))
	case n < 0 || (n >= 2 && d.b[n-1] == 0):
		return 0, badState("%s: bad varint", stateField(field, index))
	}
	d.b = d.b[n:]
	return x, nil
}

func (d *stateDecoder) ref(field string, index int) (chain.BlockRef, error) {
	var r chain.BlockRef
	var err error
	if r.Number, err = d.uvarint(field, index); err != nil {
		return chain.BlockRef{}, err
	}
	if len(d.b) < 2*common.HashLength {
		return chain.BlockRef{}, badState("%s: truncated", stateField(field, index))
	}
	copy(r.Hash[:], d.b)
	copy(r.Parent[:], d.b[common.HashLength:])
	d.b = d.b[2*common.HashLength:]
	if r.Time, err = d.uvarint(field, index); err != nil {
		return chain.BlockRef{}, err
	}
	return r, nil
}

// stateField returns field, or "<field>[<index>]" for index >= 0.
func stateField(field string, index int) string {
	if index < 0 {
		return field
	}
	return field + "[" + strconv.Itoa(index) + "]"
}

func badState(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadState, fmt.Sprintf(format, args...))
}
