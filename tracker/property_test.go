// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
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
// and Finalize of Finalized. Every non-empty result is appended to a log, which must accept it,
// and after every call, whatever it returned, the tracker's State must equal the log's.
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
	logView := viewOf(chainlog.NewState(tracker.DefaultWindowCap))
	var run propertyRun

	apply := func(step int, what string, got []chainlog.Entry, err error) {
		t.Helper()
		if err != nil {
			if !errors.Is(err, chain.ErrNotFound) {
				t.Fatalf("seed %d step %d: %s: %v", seed, step, what, err)
			}
			run.results = append(run.results, result{err: err.Error()})
		} else {
			run.results = append(run.results, result{entries: got})
		}
		if len(got) > 0 {
			if _, aerr := lg.Append(ctx, got...); aerr != nil {
				t.Fatalf("seed %d step %d: %s: Append: %v", seed, step, what, aerr)
			}
			ls, serr := lg.State(ctx)
			if serr != nil {
				t.Fatal(serr)
			}
			logView = viewOf(ls)
		}
		if tv := viewOf(tr.State()); !sameView(tv, logView) {
			t.Fatalf("seed %d step %d: %s: tracker state %+v, log state %+v", seed, step, what, tv, logView)
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

// sameView compares two views without reflection, which dominated the property test's run time.
func sameView(a, b view) bool {
	return a.Tip == b.Tip && a.Finalized == b.Finalized && slices.Equal(a.Window, b.Window)
}

// firstDiff returns the first index at which two sequences of lengths na and nb differ, where
// same(i) compares element i of both, or -1 when they are equal.
func firstDiff(na, nb int, same func(i int) bool) int {
	for i := range min(na, nb) {
		if !same(i) {
			return i
		}
	}
	if na != nb {
		return min(na, nb)
	}
	return -1
}

// at returns element i of s formatted with %+v, or "nothing" when s is shorter.
func at[T any](s []T, i int) string {
	if i < len(s) {
		return fmt.Sprintf("%+v", s[i])
	}
	return "nothing"
}

func TestMutualConsistency(t *testing.T) {
	seeds := uint64(300)
	if testing.Short() {
		seeds = 50
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			first := runSeed(t, seed)
			second := runSeed(t, seed)
			if i := firstDiff(len(first.calls), len(second.calls), func(i int) bool {
				return first.calls[i] == second.calls[i]
			}); i >= 0 {
				t.Fatalf("Source call %d differs: first run %s, repeated run %s", i, at(first.calls, i), at(second.calls, i))
			}
			if i := firstDiff(len(first.results), len(second.results), func(i int) bool {
				return reflect.DeepEqual(first.results[i], second.results[i])
			}); i >= 0 {
				t.Fatalf("result %d differs: first run %s, repeated run %s", i, at(first.results, i), at(second.results, i))
			}
		})
	}
}
