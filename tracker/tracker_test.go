// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/internal/fakechain"
	"github.com/hmdsefi/evmsync/tracker"
)

func TestAnchoring(t *testing.T) {
	e := newBareEnv(t, tracker.Config{}, fakechain.Faults{}, nil)
	a := e.mine("A", 10)
	if got, want := viewOf(e.tr.State()), viewOf(chainlog.NewState(tracker.DefaultWindowCap)); !reflect.DeepEqual(got, want) {
		t.Errorf("unanchored state = %+v, want %+v", got, want)
	}

	got, err := e.head(t, a[2])
	wantErr(t, err, tracker.ErrUnanchored, "tracker: no finalized block: head 3 "+a[2].Hash.Hex())
	e.want(t, got)
	e.wantCalls(t, "")

	got = e.mustFinalize(t, a[6])
	e.want(t, got, e.fin(a[6]))
	e.wantCalls(t, "")
	st := e.tr.State()
	if st.Tip() != a[6] || st.Finalized() != a[6] || st.Window() != nil {
		t.Errorf("state = %+v, want tip and finalized A7, empty window", viewOf(st))
	}

	got = e.mustHead(t, a[9])
	e.want(t, got, e.blks(a[7], a[8], a[9])...)
	e.wantCalls(t, "H(A9) H(A8) L(A8) L(A9) L(A10)")
}

func TestNewPanics(t *testing.T) {
	e := newBareEnv(t, tracker.Config{}, fakechain.Faults{}, nil)
	a := e.mine("A", 4)
	stateOf := func(entries ...chainlog.Entry) chainlog.State {
		s := chainlog.NewState(100)
		for _, en := range entries {
			s.Apply(en)
		}
		return s
	}
	final := func(x chain.BlockRef) chainlog.Entry {
		en := e.blk(x)
		en.Final = true
		return en
	}
	tests := []struct {
		name string
		src  chain.Source
		cfg  tracker.Config
		st   chainlog.State
		want string
	}{
		{"nil source", nil, tracker.Config{}, stateOf(), "evmsync: tracker.New: nil Source"},
		{"negative cap", e.src, tracker.Config{WindowCap: -1}, stateOf(), "evmsync: tracker.New: negative WindowCap"},
		{
			"blocks without finalized",
			e.src, tracker.Config{}, stateOf(e.blk(a[1])),
			"evmsync: tracker.New: state has blocks but no finalized block",
		},
		{
			"window with a gap",
			e.src, tracker.Config{}, stateOf(e.fin(e.g), e.blk(a[0]), e.blk(a[2])),
			"evmsync: tracker.New: window is not a parent-linked chain above the finalized block",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustPanic(t, tt.want, func() { tracker.New(tt.src, tt.cfg, tt.st) })
		})
	}

	t.Run("backfill prefix", func(t *testing.T) {
		e.tr = tracker.New(e.src, tracker.Config{Filter: flt}, stateOf(e.fin(e.g), final(a[0]), final(a[2])))
		got, err := e.tr.Head(t.Context(), chain.Header{Ref: a[3]})
		if err != nil {
			t.Fatalf("Head(A4): %v", err)
		}
		e.want(t, got, e.blk(a[3]))
	})
}

func TestResume(t *testing.T) {
	e := newBareEnv(t, tracker.Config{}, fakechain.Faults{}, nil)
	a := e.mine("A", 4)
	if _, err := e.lg.Append(t.Context(), e.fin(e.g), e.blk(a[0]), e.blk(a[1]), e.blk(a[2])); err != nil {
		t.Fatal(err)
	}
	st, err := e.lg.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	e.tr = tracker.New(e.src, e.cfg, st)
	e.sameState(t)

	got := e.mustHead(t, a[3])
	e.want(t, got, e.blk(a[3]))
	e.wantCalls(t, "L(A4)")
}

func TestExtendByOne(t *testing.T) {
	t.Run("honest", func(t *testing.T) {
		e := newEnv(t, tracker.Config{}, fakechain.Faults{}, nil)
		a := e.mine("A", 1)
		got := e.mustHead(t, a[0])
		e.want(t, got, e.blk(a[0]))
		e.wantCalls(t, "L(A1)")
	})
	t.Run("logs kept as returned", func(t *testing.T) {
		var ls []chain.Log
		e := newEnv(t, tracker.Config{}, fakechain.Faults{}, func(s chain.Source) chain.Source {
			return &wrapSource{Source: s, logs: func(context.Context, common.Hash, chain.Filter) ([]chain.Log, error) {
				return ls, nil
			}}
		})
		a := e.mine("A", 1)
		ls = []chain.Log{{
			Address:     fakechain.Address(0),
			Topics:      []common.Hash{fakechain.TransferTopic},
			BlockNumber: a[0].Number,
			BlockHash:   a[0].Hash,
		}}
		got := e.mustHead(t, a[0])
		if len(got) != 1 || len(got[0].Block.Logs) != 1 || &got[0].Block.Logs[0] != &ls[0] {
			t.Fatalf("entries = %+v, want one block whose Logs is the returned slice", got)
		}
	})
	t.Run("filter passed unchanged", func(t *testing.T) {
		cfgFilter := chain.Filter{Addresses: []common.Address{fakechain.Address(2)}}
		var seen []chain.Filter
		e := newEnv(t, tracker.Config{Filter: cfgFilter}, fakechain.Faults{}, func(s chain.Source) chain.Source {
			return &wrapSource{Source: s, logs: func(ctx context.Context, h common.Hash, f chain.Filter) ([]chain.Log, error) {
				seen = append(seen, f)
				return s.Logs(ctx, h, f)
			}}
		})
		a := e.mine("A", 2)
		e.mustHead(t, a[1])
		if want := []chain.Filter{cfgFilter, cfgFilter}; !reflect.DeepEqual(seen, want) {
			t.Errorf("Logs filters = %v, want %v", seen, want)
		}
	})
	t.Run("nil logs stay nil", func(t *testing.T) {
		e := newEnv(t, tracker.Config{}, fakechain.Faults{}, func(s chain.Source) chain.Source {
			return &wrapSource{Source: s, logs: func(context.Context, common.Hash, chain.Filter) ([]chain.Log, error) {
				return nil, nil
			}}
		})
		a := e.mine("A", 1)
		got := e.mustHead(t, a[0])
		if len(got) != 1 || got[0].Block.Logs != nil {
			t.Fatalf("entries = %+v, want one block with nil Logs", got)
		}
	})
}

func TestDuplicates(t *testing.T) {
	e := tracked(t, 6)
	before := viewOf(e.tr.State())
	for _, h := range []chain.BlockRef{e.refs(6, 6)[0], e.refs(3, 3)[0], e.g} {
		got := e.mustHead(t, h)
		e.want(t, got)
		e.wantCalls(t, "")
	}
	if after := viewOf(e.tr.State()); !reflect.DeepEqual(after, before) {
		t.Errorf("state changed: %+v, want %+v", after, before)
	}
}

func TestLaggingProvider(t *testing.T) {
	e := tracked(t, 8)
	e.mustFinalize(t, e.refs(5, 5)[0])
	for _, h := range e.refs(4, 5) {
		e.want(t, e.mustHead(t, h))
		e.wantCalls(t, "")
	}
	b := e.reorg("B", 6, 6)
	for _, h := range b[1:3] { // B4, B5
		e.want(t, e.mustHead(t, h))
		e.wantCalls(t, "")
	}
}

func TestGapFill(t *testing.T) {
	e := tracked(t, 2)
	e.mine("A", 4)
	a := e.refs(3, 6)
	got := e.mustHead(t, a[3])
	e.want(t, got, e.blks(a...)...)
	e.wantCalls(t, "H(A5) H(A4) H(A3) L(A3) L(A4) L(A5) L(A6)")
}

func TestDepthOneReorg(t *testing.T) {
	e := tracked(t, 5)
	b := e.reorg("B", 1, 1)
	got := e.mustHead(t, b[0])
	e.want(t, got, e.rev(e.refs(4, 4)[0]), e.blk(b[0]))
	e.wantCalls(t, "L(B5)")
}

func TestDepthNReorg(t *testing.T) {
	e := tracked(t, 10)
	a6 := e.refs(6, 6)[0]
	b := e.reorg("B", 4, 4)
	got := e.mustHead(t, b[3])
	e.want(t, got, append([]chainlog.Entry{e.rev(a6)}, e.blks(b...)...)...)
	e.wantCalls(t, "H(B9) H(B8) H(B7) L(B7) L(B8) L(B9) L(B10)")
}

func TestReorgDuringGapFill(t *testing.T) {
	e := tracked(t, 5)
	a3 := e.refs(3, 3)[0]
	b := e.reorg("B", 2, 5)
	got := e.mustHead(t, b[4])
	e.want(t, got, append([]chainlog.Entry{e.rev(a3)}, e.blks(b...)...)...)
	e.wantCalls(t, "H(B7) H(B6) H(B5) H(B4) L(B4) L(B5) L(B6) L(B7) L(B8)")
}

func TestShorterBranch(t *testing.T) {
	e := tracked(t, 6)
	a4 := e.refs(4, 4)[0]
	b5 := e.reorg("B", 2, 1)[0]
	e.want(t, e.mustHead(t, b5))
	e.wantCalls(t, "")
	b6 := e.mine("B", 1)[0]
	got := e.mustHead(t, b6)
	e.want(t, got, e.rev(a4), e.blk(b5), e.blk(b6))
	e.wantCalls(t, "H(B5) L(B5) L(B6)")
}

func TestSameHeightSibling(t *testing.T) {
	e := tracked(t, 6)
	a := e.refs(5, 6)
	b6 := e.reorg("B", 1, 1)[0]
	e.want(t, e.mustHead(t, b6), e.rev(a[0]), e.blk(b6))
	got := e.mustHead(t, a[1])
	e.want(t, got, e.rev(a[0]), e.blk(a[1]))
	e.wantCalls(t, "L(A6)")
}

func TestRollback(t *testing.T) {
	e := tracked(t, 6)
	a3 := e.refs(3, 3)[0]
	e.reorg("A", 3, 0)
	e.want(t, e.mustHead(t, a3))
	e.wantCalls(t, "")
	c := e.mine("C", 2)
	for _, h := range c {
		e.want(t, e.mustHead(t, h))
		e.wantCalls(t, "")
	}
	c6 := e.mine("C", 1)[0]
	got := e.mustHead(t, c6)
	e.want(t, got, e.rev(a3), e.blk(c[0]), e.blk(c[1]), e.blk(c6))
	e.wantCalls(t, "H(C5) H(C4) L(C4) L(C5) L(C6)")
}

func TestWalkReachesFinalized(t *testing.T) {
	e := tracked(t, 8)
	a5 := e.refs(5, 5)[0]
	e.mustFinalize(t, a5)
	before := viewOf(e.tr.State())
	b := e.reorg("B", 5, 6) // B4…B9
	got, err := e.head(t, b[5])
	wantErr(t, err, tracker.ErrReorgBelowFinalized, "tracker: reorg below the finalized block: block 6 "+
		b[2].Hash.Hex()+" does not descend from finalized block 5 "+a5.Hash.Hex())
	e.want(t, got)
	e.wantCalls(t, "H(B8) H(B7) H(B6)")
	if after := viewOf(e.tr.State()); !reflect.DeepEqual(after, before) {
		t.Errorf("state changed: %+v, want %+v", after, before)
	}
}

func TestDroppedFork(t *testing.T) {
	e := newEnv(t, tracker.Config{}, fakechain.Faults{DropOrphans: true}, nil)
	a := e.mine("A", 5)
	e.mustHead(t, a[4])
	b := e.reorg("B", 2, 2)
	hB := b[1]
	c := e.reorg("C", 3, 4) // C3…C6
	before := viewOf(e.tr.State())

	got, err := e.head(t, hB)
	wantErr(t, err, chain.ErrNotFound, "tracker: header "+b[0].Hash.Hex()+" (block 4): fakechain: block "+
		b[0].Hash.Hex()+": "+chain.ErrNotFound.Error())
	e.want(t, got)
	e.wantCalls(t, "H(B4)")
	if after := viewOf(e.tr.State()); !reflect.DeepEqual(after, before) {
		t.Errorf("state changed: %+v, want %+v", after, before)
	}

	got = e.mustHead(t, c[3])
	e.want(t, got, append([]chainlog.Entry{e.rev(a[1])}, e.blks(c...)...)...)
}

// failingSource passes every call to the Source it wraps, and replaces the result of the
// failAt-th HeaderByHash or Logs call with errors.New("boom"). failAt 0 fails nothing.
type failingSource struct {
	chain.Source
	failAt, n int
}

var errBoom = errors.New("boom")

func (f *failingSource) fail() bool {
	f.n++
	return f.n == f.failAt
}

func (f *failingSource) HeaderByHash(ctx context.Context, h common.Hash) (chain.Header, error) {
	hd, err := f.Source.HeaderByHash(ctx, h)
	if f.fail() {
		return chain.Header{}, errBoom
	}
	return hd, err
}

func (f *failingSource) Logs(ctx context.Context, h common.Hash, flt chain.Filter) ([]chain.Log, error) {
	ls, err := f.Source.Logs(ctx, h, flt)
	if f.fail() {
		return nil, errBoom
	}
	return ls, err
}

func TestErrorsKeepState(t *testing.T) {
	healthyCalls := []string{"H(B7)", "H(B6)", "H(B5)", "H(B4)", "L(B4)", "L(B5)", "L(B6)", "L(B7)", "L(B8)"}
	setup := func(t *testing.T) (*env, *failingSource, []chain.BlockRef) {
		t.Helper()
		var fs *failingSource
		e := newEnv(t, tracker.Config{}, fakechain.Faults{}, func(s chain.Source) chain.Source {
			fs = &failingSource{Source: s}
			return fs
		})
		a := e.mine("A", 5)
		e.mustHead(t, a[4])
		return e, fs, e.reorg("B", 2, 5)
	}
	for k := 1; k <= len(healthyCalls); k++ {
		t.Run(healthyCalls[k-1], func(t *testing.T) {
			e, fs, b := setup(t)
			before := viewOf(e.tr.State())
			fs.failAt, fs.n = k, 0
			got, err := e.head(t, b[4])
			if err == nil || !errors.Is(err, errBoom) || !strings.HasSuffix(err.Error(), ": boom") {
				t.Fatalf("err = %v, want one ending in \": boom\"", err)
			}
			e.want(t, got)
			if calls := strings.Join(e.calls(), " "); calls != strings.Join(healthyCalls[:k], " ") {
				t.Errorf("calls = %q, want %q", calls, strings.Join(healthyCalls[:k], " "))
			}
			if after := viewOf(e.tr.State()); !reflect.DeepEqual(after, before) {
				t.Errorf("state changed: %+v, want %+v", after, before)
			}
			fs.failAt = 0
			got = e.mustHead(t, b[4])
			e.want(t, got, append([]chainlog.Entry{e.rev(e.refs(3, 3)[0])}, e.blks(b...)...)...)
		})
	}
	t.Run("context canceled during the walk", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var b4 common.Hash
		e := newEnv(t, tracker.Config{}, fakechain.Faults{}, func(s chain.Source) chain.Source {
			return &wrapSource{Source: s, header: func(ctx context.Context, h common.Hash) (chain.Header, error) {
				hd, err := s.HeaderByHash(ctx, h)
				if h == b4 {
					cancel()
				}
				return hd, err
			}}
		})
		a := e.mine("A", 5)
		e.mustHead(t, a[4])
		b := e.reorg("B", 2, 5)
		b4 = b[0].Hash
		before := viewOf(e.tr.State())
		e.src.ResetCalls()
		got, err := e.tr.Head(ctx, chain.Header{Ref: b[4]})
		wantErr(t, err, context.Canceled, "tracker: context canceled")
		e.want(t, got)
		e.wantCalls(t, "H(B7) H(B6) H(B5) H(B4)")
		if after := viewOf(e.tr.State()); !reflect.DeepEqual(after, before) {
			t.Errorf("state changed: %+v, want %+v", after, before)
		}
	})
	t.Run("canceled context", func(t *testing.T) {
		e, _, b := setup(t)
		before := viewOf(e.tr.State())
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		e.src.ResetCalls()
		got, err := e.tr.Head(ctx, chain.Header{Ref: b[4]})
		wantErr(t, err, context.Canceled, "tracker: context canceled")
		e.want(t, got)
		e.wantCalls(t, "")
		if after := viewOf(e.tr.State()); !reflect.DeepEqual(after, before) {
			t.Errorf("state changed: %+v, want %+v", after, before)
		}
	})
}

func TestInconsistentSource(t *testing.T) {
	type answer func(e *env, h common.Hash) (chain.Header, error)
	setup := func(t *testing.T, ans answer) *env {
		t.Helper()
		var e *env
		e = newEnv(t, tracker.Config{}, fakechain.Faults{}, func(s chain.Source) chain.Source {
			return &wrapSource{Source: s, header: func(ctx context.Context, h common.Hash) (chain.Header, error) {
				if ans != nil {
					if hd, err := ans(e, h); err != nil || hd != (chain.Header{}) {
						return hd, err
					}
				}
				return s.HeaderByHash(ctx, h)
			}}
		})
		a := e.mine("A", 5)
		e.mustHead(t, a[4])
		e.mine("A", 3)
		return e
	}
	headerOf := func(e *env, n uint64) chain.Header {
		h, _ := e.c.Canonical(n)
		return h
	}
	tests := []struct {
		name string
		ans  answer
		head func(e *env) chain.Header
		want func(e *env) string
	}{
		{
			name: "wrong hash",
			ans: func(e *env, h common.Hash) (chain.Header, error) {
				if h == headerOf(e, 7).Ref.Hash {
					return headerOf(e, 6), nil
				}
				return chain.Header{}, nil
			},
			head: func(e *env) chain.Header { return headerOf(e, 8) },
			want: func(e *env) string {
				return "tracker: inconsistent source: header " + headerOf(e, 7).Ref.Hash.Hex() +
					": got hash " + headerOf(e, 6).Ref.Hash.Hex()
			},
		},
		{
			name: "wrong number",
			ans: func(e *env, h common.Hash) (chain.Header, error) {
				if hd := headerOf(e, 7); h == hd.Ref.Hash {
					hd.Ref.Number = 9
					return hd, nil
				}
				return chain.Header{}, nil
			},
			head: func(e *env) chain.Header { return headerOf(e, 8) },
			want: func(e *env) string {
				return "tracker: inconsistent source: header " + headerOf(e, 7).Ref.Hash.Hex() + ": got number 9, want 7"
			},
		},
		{
			name: "known parent at the wrong height",
			head: func(e *env) chain.Header {
				return chain.Header{Ref: chain.BlockRef{Number: 8, Parent: headerOf(e, 3).Ref.Hash, Hash: common.HexToHash("0xf8")}}
			},
			want: func(e *env) string {
				return "tracker: inconsistent source: block 8 " + common.HexToHash("0xf8").Hex() + " has parent " +
					headerOf(e, 3).Ref.Hash.Hex() + " at height 3"
			},
		},
		{
			name: "zero hash",
			head: func(*env) chain.Header { return chain.Header{} },
			want: func(*env) string { return "tracker: inconsistent source: zero block hash" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, tt.ans)
			before := viewOf(e.tr.State())
			got, err := e.headHeader(t, tt.head(e))
			wantErr(t, err, tracker.ErrInconsistentSource, tt.want(e))
			e.want(t, got)
			if after := viewOf(e.tr.State()); !reflect.DeepEqual(after, before) {
				t.Errorf("state changed: %+v, want %+v", after, before)
			}
		})
	}
}

func TestFinalizeInWindow(t *testing.T) {
	e := tracked(t, 6)
	a := e.refs(1, 6)
	got := e.mustFinalize(t, a[2])
	e.want(t, got, e.fin(a[2]))
	e.wantCalls(t, "")
	if w := e.tr.State().Window(); !reflect.DeepEqual(w, a[3:]) {
		t.Errorf("window = %v, want A4…A6", w)
	}
	e.want(t, e.mustFinalize(t, a[1]))
	e.want(t, e.mustFinalize(t, a[2]))

	other := chain.BlockRef{Number: 3, Hash: common.HexToHash("0x03")}
	got, err := e.finalize(t, other)
	wantErr(t, err, tracker.ErrReorgBelowFinalized, "tracker: reorg below the finalized block: finalized block 3 is "+
		a[2].Hash.Hex()+", got "+other.Hash.Hex())
	e.want(t, got)

	wrong := a[4]
	wrong.Number = 4
	got, err = e.finalize(t, wrong)
	wantErr(t, err, tracker.ErrInconsistentSource, "tracker: inconsistent source: block "+a[4].Hash.Hex()+
		" is at height 5, got 4")
	e.want(t, got)

	got, err = e.finalize(t, chain.BlockRef{Number: 7})
	wantErr(t, err, tracker.ErrInconsistentSource, "tracker: inconsistent source: zero block hash")
	e.want(t, got)
}

func TestFinalizeAboveTip(t *testing.T) {
	e := tracked(t, 3)
	e.mine("A", 5)
	a := e.refs(4, 8)
	got := e.mustFinalize(t, a[2])
	e.want(t, got, e.blk(a[0]), e.blk(a[1]), e.blk(a[2]), e.fin(a[2]))
	e.wantCalls(t, "H(A5) H(A4) L(A4) L(A5) L(A6)")
	if st := e.tr.State(); st.Tip() != a[2] || st.Finalized() != a[2] || st.Window() != nil {
		t.Errorf("state = %+v, want tip and finalized A6, empty window", viewOf(st))
	}
	e.want(t, e.mustHead(t, a[4]), e.blk(a[3]), e.blk(a[4]))
}

func TestFinalizeDisagrees(t *testing.T) {
	e := tracked(t, 6)
	a3 := e.refs(3, 3)[0]
	b := e.reorg("B", 3, 3) // B4…B6
	got := e.mustFinalize(t, b[1])
	e.want(t, got, e.rev(a3), e.blk(b[0]), e.blk(b[1]), e.fin(b[1]))
	e.wantCalls(t, "H(B4) L(B4) L(B5)")
	if st := e.tr.State(); st.Tip() != b[1] || st.Finalized() != b[1] {
		t.Errorf("state = %+v, want tip and finalized B5", viewOf(st))
	}
	e.want(t, e.mustHead(t, b[2]), e.blk(b[2]))

	c := e.reorg("C", 4, 5) // C3…C7
	e.src.Do(func(ch *fakechain.Chain) { ch.SetFinalized(4) })
	e.want(t, e.mustFinalize(t, c[1]))

	got, err := e.finalize(t, c[2])
	wantErr(t, err, tracker.ErrReorgBelowFinalized, "tracker: reorg below the finalized block: finalized block 5 is "+
		b[1].Hash.Hex()+", got "+c[2].Hash.Hex())
	e.want(t, got)

	got, err = e.finalize(t, c[4])
	wantErr(t, err, tracker.ErrReorgBelowFinalized, "tracker: reorg below the finalized block: block 6 "+
		c[3].Hash.Hex()+" does not descend from finalized block 5 "+b[1].Hash.Hex())
	e.want(t, got)
	e.wantCalls(t, "H(C6)")
}
