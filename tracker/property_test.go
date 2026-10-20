// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/internal/fakechain"
	"github.com/hmdsefi/evmsync/tracker"
)

func TestStateSnapshots(t *testing.T) {
	e := tracked(t, 4)
	a := e.refs(1, 4)
	s := e.tr.State()

	a5 := e.mine("A", 1)[0]
	e.mustHead(t, a5)
	e.mustFinalize(t, a[1])

	if got, want := viewOf(s), (view{Tip: a[3], Finalized: e.g, Window: a}); !reflect.DeepEqual(got, want) {
		t.Errorf("earlier snapshot = %+v, want %+v", got, want)
	}
	want := view{Tip: a5, Finalized: a[1], Window: []chain.BlockRef{a[2], a[3], a5}}
	if got := viewOf(e.tr.State()); !reflect.DeepEqual(got, want) {
		t.Errorf("State() = %+v, want %+v", got, want)
	}
	e.sameState(t)
}

// propertyRun is the record of one seed's run: every Source call, and every call's entries or
// error text, in order.
type propertyRun struct {
	calls   []fakechain.Call
	results []result
}

// result is what one Head or Finalize call returned.
type result struct {
	entries []chainlog.Entry
	err     string
}

// runSeed drives a tracker over a faulty source for seed: 300 iterations of Step, Head of Latest
// and Finalize of Finalized. Every result is appended to a log, which must accept it, and the
// tracker's State must equal the log's after every call.
func runSeed(t *testing.T, seed uint64) propertyRun {
	t.Helper()
	ctx := t.Context()
	c := fakechain.New(seed, fakechain.Params{})
	src := fakechain.NewSource(c, seed, fakechain.Faults{
		StaleHeadPPM:   100_000,
		StaleOrphanPPM: 100_000,
		DropOrphans:    seed%2 == 0,
		InterleavePPM:  100_000,
	})
	lg := chainlog.NewMemory(c.Meta(flt), tracker.DefaultWindowCap)
	tr := tracker.New(src, tracker.Config{Filter: flt}, chainlog.NewState(tracker.DefaultWindowCap))
	var run propertyRun

	apply := func(step int, what string, got []chainlog.Entry, err error) {
		t.Helper()
		if err != nil {
			if !errors.Is(err, chain.ErrNotFound) {
				t.Fatalf("seed %d step %d: %s: %v", seed, step, what, err)
			}
			run.results = append(run.results, result{err: err.Error()})
			return
		}
		run.results = append(run.results, result{entries: got})
		if len(got) == 0 {
			return
		}
		if _, aerr := lg.Append(ctx, got...); aerr != nil {
			t.Fatalf("seed %d step %d: %s: Append: %v", seed, step, what, aerr)
		}
		ls, serr := lg.State(ctx)
		if serr != nil {
			t.Fatal(serr)
		}
		if tv, lv := viewOf(tr.State()), viewOf(ls); !reflect.DeepEqual(tv, lv) {
			t.Fatalf("seed %d step %d: %s: tracker state %+v, log state %+v", seed, step, what, tv, lv)
		}
	}

	got, err := tr.Finalize(ctx, c.Genesis().Ref)
	apply(0, "Finalize(genesis)", got, err)
	for step := 1; step <= 300; step++ {
		src.Do(func(c *fakechain.Chain) { c.Step() })
		h, err := src.Latest(ctx)
		if err != nil {
			t.Fatalf("seed %d step %d: Latest: %v", seed, step, err)
		}
		got, err := tr.Head(ctx, h)
		apply(step, "Head", got, err)
		f, err := src.Finalized(ctx)
		if err != nil {
			t.Fatalf("seed %d step %d: Finalized: %v", seed, step, err)
		}
		got, err = tr.Finalize(ctx, f.Ref)
		apply(step, "Finalize", got, err)
	}
	run.calls = src.Calls()
	return run
}

func TestMutualConsistency(t *testing.T) {
	seeds := uint64(300)
	if testing.Short() {
		seeds = 50
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		first := runSeed(t, seed)
		second := runSeed(t, seed)
		if !reflect.DeepEqual(first.calls, second.calls) {
			t.Fatalf("seed %d: the repeated run made different Source calls", seed)
		}
		if !reflect.DeepEqual(first.results, second.results) {
			t.Fatalf("seed %d: the repeated run returned different results", seed)
		}
	}
}
