// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/internal/fakechain"
	"github.com/hmdsefi/evmsync/tracker"
)

func TestWindowCap(t *testing.T) {
	t.Run("cap 4", func(t *testing.T) {
		e := newEnv(t, tracker.Config{WindowCap: 4}, fakechain.Faults{}, nil)
		a := e.mine("A", 8)
		e.want(t, e.mustHead(t, a[3]), e.blks(a[:4]...)...)
		// State carries the tracker's cap: its window is full too.
		st := e.tr.State()
		if err := st.Check(e.blk(a[4])); !errors.Is(err, chainlog.ErrMalformed) {
			t.Errorf("State().Check(blk(A5)) = %v, want an error wrapping chainlog.ErrMalformed", err)
		}

		got, err := e.head(t, a[4])
		wantErr(t, err, tracker.ErrWindowFull,
			"tracker: unfinalized window full: block 5 is 5 blocks above finalized block 0 (cap 4)")
		e.want(t, got)
		e.wantCalls(t, "")

		e.want(t, e.mustFinalize(t, a[1]), e.fin(a[1]))
		e.wantCalls(t, "")
		e.want(t, e.mustHead(t, a[5]), e.blk(a[4]), e.blk(a[5]))

		before := viewOf(e.tr.State())
		got, err = e.head(t, a[7])
		wantErr(t, err, tracker.ErrWindowFull,
			"tracker: unfinalized window full: block 8 is 6 blocks above finalized block 2 (cap 4)")
		e.want(t, got)
		e.wantCalls(t, "")

		got, err = e.finalize(t, a[7])
		wantErr(t, err, tracker.ErrWindowFull,
			"tracker: unfinalized window full: block 8 is 6 blocks above finalized block 2 (cap 4)")
		e.want(t, got)
		e.wantCalls(t, "")
		if after := viewOf(e.tr.State()); !reflect.DeepEqual(after, before) {
			t.Errorf("state changed: %+v, want %+v", after, before)
		}
	})
	t.Run("default cap", func(t *testing.T) {
		e := newEnv(t, tracker.Config{WindowCap: 0}, fakechain.Faults{}, nil)
		a := e.mine("A", tracker.DefaultWindowCap+1)
		got := e.mustHead(t, a[tracker.DefaultWindowCap-1])
		if len(got) != tracker.DefaultWindowCap {
			t.Fatalf("Head(block %d) returned %d entries, want %d", tracker.DefaultWindowCap, len(got), tracker.DefaultWindowCap)
		}
		got, err := e.head(t, a[tracker.DefaultWindowCap])
		wantErr(t, err, tracker.ErrWindowFull,
			"tracker: unfinalized window full: block 100001 is 100001 blocks above finalized block 0 (cap 100000)")
		e.want(t, got)
		e.wantCalls(t, "")
	})
}

// TestNewOverCap resumes a tracker with cap 4 over a log whose window holds 6 blocks.
func TestNewOverCap(t *testing.T) {
	e := newBareEnv(t, tracker.Config{}, fakechain.Faults{}, nil)
	a := e.mine("A", 7)
	if _, err := e.lg.Append(t.Context(), append([]chainlog.Entry{e.fin(e.g)}, e.blks(a[:6]...)...)...); err != nil {
		t.Fatal(err)
	}
	st, err := e.lg.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	e.tr = tracker.New(e.src, tracker.Config{Filter: flt, WindowCap: 4}, st)
	e.sameState(t)

	got, err := e.head(t, a[6])
	wantErr(t, err, tracker.ErrWindowFull,
		"tracker: unfinalized window full: block 7 is 7 blocks above finalized block 0 (cap 4)")
	e.want(t, got)
	e.wantCalls(t, "")

	// A duplicate head and a head behind the tip are ignored before the cap is checked.
	sibling := chain.BlockRef{Number: 5, Hash: common.HexToHash("0x05"), Parent: a[3].Hash}
	for _, h := range []chain.BlockRef{a[5], sibling} {
		e.want(t, e.mustHead(t, h))
		e.wantCalls(t, "")
	}

	// A Finalize inside the window never fails the cap check, even 5 blocks above F with cap 4.
	e.want(t, e.mustFinalize(t, a[4]), e.fin(a[4]))
	e.wantCalls(t, "")
	e.want(t, e.mustHead(t, a[6]), e.blk(a[6]))
	e.wantCalls(t, "L(A7)")
}
